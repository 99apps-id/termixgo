// Package app wires the pieces together: configuration, secrets, providers,
// skills, the agent runner and the Telegram companion. The terminal UI and
// the bot both drive this one object, so a prompt means the same thing
// wherever it is typed.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/mcp"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/search"
	"github.com/99apps-id/termixgo/internal/secrets"
	"github.com/99apps-id/termixgo/internal/skill"
	"github.com/99apps-id/termixgo/internal/telegram"
)

// eventBuffer is deep enough that a burst of tool output never blocks a run.
const eventBuffer = 512

// mcpConnectTimeout bounds the MCP handshake and tool listing together. A server
// that hangs on startup must not stop the session from opening, so the whole
// attempt is capped rather than each server separately.
const mcpConnectTimeout = 20 * time.Second

// ErrBusy reports that a turn is already in flight. Callers surface it rather
// than queueing, so a second surface never appears to hang.
var ErrBusy = errors.New("a turn is already running; stop it first")

// ErrNoModel reports that onboarding has not finished.
var ErrNoModel = errors.New("no model is configured; run /setup to add a provider key and pick a model")

// Interactor is the UI side of the agent: approvals and questions. When it is
// nil, approvals are denied and questions fail, which is the safe default for
// a headless run.
type Interactor interface {
	Approve(request agent.ApprovalRequest) agent.Decision
	Ask(question string, options []string) (string, error)
}

// App is the shared, concurrency-safe application state.
type App struct {
	mu sync.Mutex

	cfg       config.Config
	store     *secrets.Store
	workspace string

	trusted bool
	skills  []skill.Skill
	memory  *agent.Memory
	todos   *agent.TodoStore
	tools   *agent.Registry
	session *agent.Session

	// search is the full-text index behind search_memory. It is nil when the
	// index could not be opened, which only degrades that one tool to the
	// substring fallback rather than failing the session.
	search *search.Store

	// processes owns the background processes. It lives on the app, not on one
	// run's environment, because a dev server must outlive the turn that
	// started it.
	processes *agent.ProcessManager

	client    provider.Client
	model     provider.Model
	wireModel string
	// modelErr is why the configured model has no usable client. Without it the
	// only symptom was ErrNoModel, which read as "nothing is configured" even
	// when a model id was set but its key or endpoint was missing.
	modelErr error
	policy   *agent.ApprovalPolicy
	usage    provider.Usage
	// pricing is the resolved rate for the current model, and costKnown says
	// whether it is a real figure. Both are resolved when the model is chosen
	// rather than after the first usage event, so /cost is consistent from the
	// first moment. A local model is known-free, which is not "unknown".
	pricing   provider.Pricing
	costKnown bool
	// costUSD is the estimated spend across the live session, mirrored from
	// the runner so the status bar and /cost can show it without a lookup.
	costUSD float64

	// ephemeral suppresses session files. A one-shot `termixgo run` must not
	// leave a conversation behind on every invocation.
	ephemeral bool

	interactor Interactor
	observer   func(agent.Event)

	events  chan agent.Event
	runMu   sync.Mutex
	cancel  context.CancelFunc
	running bool
	// steer holds operator messages typed while a turn is running. The runner
	// drains it at each step boundary so a steer can change the course of the
	// turn in flight; whatever is left when the turn ends is returned to the
	// caller instead of being dropped.
	steer []string

	bot       *telegram.Bot
	botCancel context.CancelFunc
	botStatus string

	// mcp owns the connected MCP servers and the tools they contribute. It is
	// replaced rather than mutated on a reload, so a turn already in flight
	// keeps the registry it started with.
	mcp *mcp.Pool

	// journal records recurring tool failures so the agent can learn from them.
	journal *agent.ErrorJournal

	// toolCalls ledgers finished tool calls for /cost. It lives beside usage
	// because money comes from the price table while the shape of a turn
	// comes from here.
	toolCalls map[string]*ToolStat
}

// New loads the state for a workspace and prepares a session.
func New(workspace string) (*App, error) {
	absolute, err := os.Getwd()
	if err == nil && strings.TrimSpace(workspace) == "" {
		workspace = absolute
	}
	workspace = config.CleanFolder(workspace)

	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	store, err := secrets.Load()
	if err != nil {
		return nil, err
	}
	skills, err := skill.Discover(workspace)
	if err != nil {
		skills = nil
	}

	instance := &App{
		cfg:       cfg,
		store:     store,
		workspace: workspace,
		trusted:   cfg.IsTrusted(workspace),
		skills:    skills,
		memory:    agent.NewMemory(workspace),
		todos:     agent.NewTodoStore(),
		tools:     agent.DefaultRegistry(),
		processes: agent.NewProcessManager(),
		events:    make(chan agent.Event, eventBuffer),
	}
	journal, err := agent.NewErrorJournal(workspace)
	if err == nil {
		instance.journal = journal
	}
	// The manager outlives a turn, so its emitter is installed once here rather
	// than being handed a per-run environment.
	instance.processes.SetEmitter(instance.emit)
	instance.policy = &agent.ApprovalPolicy{
		Mode:           agent.ApprovalModeOrDefault(cfg),
		AlwaysAllowed:  stringSet(cfg.AlwaysAllowedTools),
		SessionAllowed: map[string]bool{},
	}
	instance.session = agent.NewSession(workspace, "")

	// The full-text index lives in the state directory inside the workspace,
	// which is where the error journal already is: the database is state, not
	// content, so it must not land in the files the model reads. A failure here
	// only degrades search_memory to its substring fallback.
	if store, err := search.OpenIn(workspace, filepath.Join(workspace, ".termixgo")); err == nil {
		instance.search = store
		agent.IndexLearnedContent(store, instance.memory, instance.journal)
	}

	// MCP servers are connected before the first turn so the model sees their
	// tools from the start. A server that fails is recorded and skipped: the
	// operator should not lose a working session to a broken server.
	instance.mcp = mcp.NewPool()
	instance.connectMCP(context.Background())

	if err := instance.restoreModel(); err != nil {
		// A missing or unusable model is not fatal: the UI sends the operator
		// to /setup, and every other feature still works. The reason is kept so
		// the bot and `serve` can name it instead of a bare ErrNoModel.
		instance.model = provider.Model{}
		instance.modelErr = err
	}
	return instance, nil
}

// connectMCP starts the configured MCP servers and folds their tools into the
// registry.
//
// It always builds a fresh registry from the built-ins, so a reload does not
// accumulate the tools of the previous attempt.
func (a *App) connectMCP(ctx context.Context) {
	cfg := a.Config()
	enabled := make([]mcp.Options, 0, len(cfg.MCPServers))
	disabled := make([]mcp.Options, 0, len(cfg.MCPServers))
	for _, server := range cfg.MCPServers {
		options := mcp.Options{
			Name:    server.Name,
			Command: server.Command,
			Args:    server.Args,
			Env:     server.Env,
			Dir:     a.workspace,
		}
		if server.Disabled {
			disabled = append(disabled, options)
			continue
		}
		enabled = append(enabled, options)
	}

	pool := mcp.NewPool()
	// The handshake and the listing are bounded so a server that hangs cannot
	// stop the session from starting.
	connectCtx, cancel := context.WithTimeout(ctx, mcpConnectTimeout)
	pool.Connect(connectCtx, disabled, enabled)
	cancel()

	contributions := make([]agent.Tool, 0, len(pool.Tools()))
	for _, bound := range pool.Tools() {
		contributions = append(contributions, agent.NewMCPTool(agent.MCPToolSpec{
			Server:      bound.Server,
			Name:        bound.Name,
			RemoteName:  bound.Tool.Name,
			Description: bound.Tool.Description,
			Schema:      bound.Tool.InputSchema,
			ReadOnly:    bound.Tool.Annotations != nil && bound.Tool.Annotations.ReadOnlyHint,
			Call:        a.mcpCaller(bound.Name),
		}))
	}

	a.mu.Lock()
	previous := a.mcp
	a.mcp = pool
	a.tools = agent.DefaultRegistry().With(contributions...)
	a.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
}

// mcpCaller binds a contributed tool name to the pool, so the tool holds no
// reference to the pool itself and a reload cannot leave it calling a closed
// one.
func (a *App) mcpCaller(name string) func(ctx context.Context, args map[string]any) (string, bool, error) {
	return func(ctx context.Context, args map[string]any) (string, bool, error) {
		a.mu.Lock()
		pool := a.mcp
		a.mu.Unlock()
		if pool == nil {
			return "", false, errors.New("no MCP server is configured")
		}
		result, err := pool.Call(ctx, name, args)
		if err != nil {
			return "", false, err
		}
		return result.Text(), result.IsError, nil
	}
}

// MCPStatus reports the configured MCP servers for the operator.
func (a *App) MCPStatus() []mcp.ServerStatus {
	a.mu.Lock()
	pool := a.mcp
	a.mu.Unlock()
	if pool == nil {
		return nil
	}
	return pool.Status()
}

// ReloadMCP reconnects every MCP server, which is what an operator does after
// fixing a command line or installing the package a server was missing.
func (a *App) ReloadMCP(ctx context.Context) error {
	if a.Running() {
		return errors.New("a turn is running; stop it before reloading MCP servers")
	}
	a.connectMCP(ctx)
	if failures := a.mcp.Failures(); len(failures) > 0 {
		return fmt.Errorf("%d MCP server(s) failed: %w", len(failures), failures[0].Err)
	}
	return nil
}

// restoreModel resolves the configured model and builds its client.
func (a *App) restoreModel() error {
	id := strings.TrimSpace(a.cfg.DefaultModel)
	if id == "" {
		return errors.New("no default model is configured")
	}
	model, ok := provider.ModelByID(id)
	if !ok {
		// A custom endpoint or a model added by hand keeps its provider from
		// the config, or falls back to the first key-based provider.
		model = provider.Model{ID: id, Label: id, Provider: a.guessProvider(id)}
	}
	return a.applyModel(model)
}

func (a *App) guessProvider(modelID string) string {
	if index := strings.Index(modelID, ":"); index > 0 {
		return modelID[:index]
	}
	// A custom endpoint is configured by base URL, not by a catalog entry or a
	// key, so an unknown model id belongs to it when one is set. Without this
	// the id fell through to a key-based provider and the run failed with a
	// missing-key error even though the endpoint was configured.
	if endpoint := strings.TrimSpace(a.cfg.BaseURLs["openai-compatible"]); endpoint != "" {
		return "openai-compatible"
	}
	for _, candidate := range provider.Providers() {
		if candidate.NeedsKey && provider.HasKey(a.store, candidate.ID) {
			return candidate.ID
		}
	}
	return "openai"
}

// applyModel validates a model, builds the client and records it.
func (a *App) applyModel(model provider.Model) error {
	info, ok := provider.ByID(model.Provider)
	if !ok {
		a.modelErr = fmt.Errorf("unknown provider %q", model.Provider)
		return a.modelErr
	}
	if info.NeedsKey && !provider.HasKey(a.store, info.ID) {
		a.modelErr = fmt.Errorf("no API key for %s yet; run /setup", info.Label)
		return a.modelErr
	}
	client, err := provider.NewClient(info.ID, provider.BaseURLFor(a.cfg, info.ID), provider.ResolverFor(a.store))
	if err != nil {
		a.modelErr = err
		return err
	}
	a.client = client
	a.model = model
	a.wireModel = provider.WireModel(a.cfg, model)
	a.pricing, a.costKnown = provider.PricedFor(a.cfg, model)
	if a.session != nil {
		a.session.SetModel(model.ID)
	}
	a.modelErr = nil
	return nil
}

// Events is the channel the UI pumps for agent activity.
func (a *App) Events() <-chan agent.Event { return a.events }

// Config returns a copy of the configuration.
func (a *App) Config() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// Secrets returns the secret store.
func (a *App) Secrets() *secrets.Store { return a.store }

// Workspace is the canonical workspace root.
func (a *App) Workspace() string { return a.workspace }

// Trusted reports the folder's trust state.
func (a *App) Trusted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.trusted
}

// CurrentModel returns the selected model.
func (a *App) CurrentModel() provider.Model {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

// Model returns the model name for the Telegram Agent interface.
func (a *App) Model() string { return a.ModelLabel() }

// SetModel switches model by free text, for the Telegram Agent interface.
func (a *App) SetModel(query string) (string, error) {
	model, err := a.SetModelByQuery(query)
	if err != nil {
		return "", err
	}
	return model.Label, nil
}

// ModelLabel is the short model name for the status bar.
func (a *App) ModelLabel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.model.Label != "" {
		return a.model.Label
	}
	return a.cfg.DefaultModel
}

// Session returns the live session.
func (a *App) Session() *agent.Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.session
}

// currentSession is the unlocked read used from inside a turn, where the
// caller has already decided the session cannot be swapped underneath it.
func (a *App) currentSession() *agent.Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.session
}

// Todos returns the plan store.
func (a *App) Todos() *agent.TodoStore { return a.todos }

// Skills returns the discovered skills.
func (a *App) Skills() []skill.Skill {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.skills
}

// Usage returns accumulated token usage.
func (a *App) Usage() provider.Usage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.usage
}

// Cost returns the estimated spend of the live session, and whether a price
// was known for the model. A free local model reports known with zero.
func (a *App) Cost() (float64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.costUSD, a.costKnown
}

// SetInteractor installs the UI callbacks.
func (a *App) SetInteractor(interactor Interactor) { a.interactor = interactor }

// SetEphemeral controls whether a turn writes a session file. One-shot runs
// set it so repeated invocations do not accumulate sessions in CI.
func (a *App) SetEphemeral(ephemeral bool) {
	a.mu.Lock()
	a.ephemeral = ephemeral
	a.mu.Unlock()
}

// Ephemeral reports whether session files are disabled.
func (a *App) Ephemeral() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ephemeral
}

// SetObserver installs a single agent-event observer. It is how the Telegram
// bridge follows a run without competing with the terminal UI for the event
// channel.
func (a *App) SetObserver(observer func(agent.Event)) {
	a.mu.Lock()
	a.observer = observer
	a.mu.Unlock()
}

// AddUsage accumulates token usage into the app and the session.
func (a *App) AddUsage(usage provider.Usage) {
	a.mu.Lock()
	a.usage = a.usage.Add(usage)
	session := a.session
	a.mu.Unlock()
	if session != nil {
		session.AddUsage(usage)
	}
}

// SetTrust updates the folder trust state and persists it.
func (a *App) SetTrust(trusted bool) error {
	a.mu.Lock()
	a.trusted = trusted
	if trusted {
		a.cfg = a.cfg.Trust(a.workspace)
	} else {
		a.cfg = a.cfg.Untrust(a.workspace)
	}
	cfg := a.cfg
	a.mu.Unlock()
	return config.Save(cfg)
}

// UpdateConfig applies a change and persists it.
func (a *App) UpdateConfig(change func(cfg *config.Config)) error {
	a.mu.Lock()
	change(&a.cfg)
	a.cfg = a.cfg.WithRecent(a.workspace)
	a.policy = &agent.ApprovalPolicy{
		Mode:           agent.ApprovalModeOrDefault(a.cfg),
		AlwaysAllowed:  a.policy.AlwaysAllowed,
		SessionAllowed: a.policy.SessionAllowed,
	}
	a.pricing, a.costKnown = provider.PricedFor(a.cfg, a.model)
	cfg := a.cfg
	a.mu.Unlock()
	return config.Save(cfg)
}

// Policy returns the live approval policy. The pointer is shared with the
// running turn on purpose: an "allow for this session" answer must be visible
// to the loop immediately. The read takes the lock so it does not race with the
// writer in AllowTool.
func (a *App) Policy() *agent.ApprovalPolicy {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.policy
}

// AllowTool permanently allows a tool across sessions.
func (a *App) AllowTool(name string) {
	a.mu.Lock()
	a.policy.AllowSession(name)
	if !containsString(a.cfg.AlwaysAllowedTools, name) {
		a.cfg.AlwaysAllowedTools = append(append([]string{}, a.cfg.AlwaysAllowedTools...), name)
	}
	cfg := a.cfg
	a.mu.Unlock()
	_ = config.Save(cfg)
}

// SetModelByQuery switches model from free text: a catalogue id, a label, a
// substring, or "provider:id" for anything not in the catalogue.
func (a *App) SetModelByQuery(query string) (provider.Model, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return provider.Model{}, errors.New("a model id is required")
	}
	var model provider.Model
	if found, ok := provider.ModelFromQuery(trimmed); ok {
		model = found
	}
	if model.ID == "" {
		// Keep the current provider and treat the text as a raw model id,
		// which is how a brand-new vendor model is used before the catalogue
		// knows it.
		current := a.CurrentModel()
		providerID := current.Provider
		if providerID == "" {
			providerID = a.guessProvider(trimmed)
		}
		model = provider.Model{ID: trimmed, Provider: providerID, Label: trimmed, APIID: trimmed}
	}
	a.mu.Lock()
	err := a.applyModel(model)
	if err == nil {
		a.cfg.DefaultModel = model.ID
	}
	cfg := a.cfg
	a.mu.Unlock()
	if err != nil {
		return provider.Model{}, err
	}
	if saveErr := config.Save(cfg); saveErr != nil {
		return model, saveErr
	}
	return model, nil
}

// SetBaseURL stores a custom endpoint for a provider, so an OpenAI-compatible
// server or an account-scoped URL has somewhere to point. The address is
// validated before it is saved: a typo here fails every later request, and the
// operator would have to go looking for the cause.
func (a *App) SetBaseURL(providerID, rawURL string) error {
	endpoint, err := provider.NormalizeBaseURL(rawURL)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if a.cfg.BaseURLs == nil {
		a.cfg.BaseURLs = map[string]string{}
	}
	a.cfg.BaseURLs[providerID] = endpoint
	cfg := a.cfg
	a.mu.Unlock()
	return config.Save(cfg)
}

// SetApprovalMode changes the approval policy.
func (a *App) SetApprovalMode(mode config.ApprovalMode) error {
	if !config.ValidApprovalMode(mode) {
		return fmt.Errorf("approval mode must be one of ask, edits, all")
	}
	a.mu.Lock()
	a.cfg.ApprovalMode = mode
	a.policy = &agent.ApprovalPolicy{
		Mode:           agent.ApprovalMode(mode),
		AlwaysAllowed:  a.policy.AlwaysAllowed,
		SessionAllowed: a.policy.SessionAllowed,
	}
	cfg := a.cfg
	a.mu.Unlock()
	return config.Save(cfg)
}

// NewSession starts a fresh conversation and clears the plan.
func (a *App) NewSession() {
	session := agent.NewSession(a.workspace, a.CurrentModel().ID)
	a.mu.Lock()
	a.session = session
	a.mu.Unlock()
	a.todos.Set(nil)
}

// LoadSession replaces the live session.
func (a *App) LoadSession(session *agent.Session) {
	if session == nil {
		return
	}
	a.mu.Lock()
	a.session = session
	a.mu.Unlock()
	a.todos.Set(session.Todos())
}

// Running reports whether a turn is in flight.
func (a *App) Running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

// Stop cancels the running turn.
func (a *App) Stop() {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Steer records an operator message for the turn in flight. A turn that has
// already stopped streaming cannot take it, so it stays queued for the caller
// to run as the next turn rather than being lost.
func (a *App) Steer(input string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return
	}
	a.mu.Lock()
	a.steer = append(a.steer, trimmed)
	a.mu.Unlock()
}

// SteerCount reports how many steers are waiting to be taken.
func (a *App) SteerCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.steer)
}

// HarnessProfile reports the harness the next turn will run under. An unset
// profile resolves to the critical default rather than to no harness at all.
func (a *App) HarnessProfile() agent.HarnessProfile {
	return agent.GetHarnessProfile(a.Config().HarnessProfile)
}

// SetHarnessProfile selects a harness profile, so the operator can trade
// thoroughness for speed without editing the config file by hand.
func (a *App) SetHarnessProfile(id string) (agent.HarnessProfile, error) {
	trimmed := strings.TrimSpace(id)
	profile, ok := agent.BuiltinHarnessProfiles[trimmed]
	if !ok {
		return agent.HarnessProfile{}, fmt.Errorf("unknown harness %q; try one of %s", trimmed, strings.Join(agent.HarnessProfileIDs(), ", "))
	}
	if err := a.UpdateConfig(func(cfg *config.Config) { cfg.HarnessProfile = profile.ID }); err != nil {
		return agent.HarnessProfile{}, err
	}
	return profile, nil
}

// TakeSteer drains the steering queue and returns what was in it. Both the
// runner and the UI call this, so whichever gets there first owns the message
// and it is never run twice.
func (a *App) TakeSteer() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.steer) == 0 {
		return nil
	}
	taken := a.steer
	a.steer = nil
	return taken
}

// env builds the per-run tool environment.
func (a *App) env() *agent.Env {
	return &agent.Env{
		Workspace: a.workspace,
		Config:    a.Config(),
		Secrets:   a.store,
		Skills:    a.Skills(),
		Memory:    a.memory,
		Todos:     a.todos,
		Trusted:   a.Trusted(),
		Processes: a.processes,
		// The journal is shared with the runner, which records into it, and read
		// back by search_memory, which is why the same store is passed here.
		Journal: a.journal,
		// The full-text index behind search_memory. Nil degrades that tool to
		// its substring fallback rather than failing the turn.
		Search:      a.search,
		Emit:        a.emit,
		Approve:     a.approve,
		Ask:         a.ask,
		RunSubagent: a.runSubagent,
	}
}

// Processes exposes the background process manager.
func (a *App) Processes() *agent.ProcessManager { return a.processes }

// Shutdown stops everything the app started. The command calls it on exit so a
// dev server does not outlive the terminal that started it.
func (a *App) Shutdown() {
	a.StopTelegram()
	if a.processes != nil {
		a.processes.Shutdown()
	}
	a.mu.Lock()
	pool := a.mcp
	a.mcp = nil
	store := a.search
	a.search = nil
	a.mu.Unlock()
	if pool != nil {
		pool.Close()
	}
	if store != nil {
		_ = store.Close()
	}
}

func (a *App) emit(event agent.Event) {
	if event.Kind == agent.EventUsage {
		a.AddUsage(event.Usage)
	}
	if event.Kind == agent.EventToolEnd {
		a.recordToolResult(event.ToolName, event.ToolOK)
	}
	switch event.Kind {
	case agent.EventUsage, agent.EventTurnEnd:
		// Every usage event carries the run's running total, so assigning is
		// correct and also repairs the value after a resumed session.
		a.mu.Lock()
		a.costUSD = event.CostUSD
		a.costKnown = event.CostKnown
		a.mu.Unlock()
	}
	a.mu.Lock()
	observer := a.observer
	a.mu.Unlock()
	if observer != nil {
		observer(event)
	}
	select {
	case a.events <- event:
	default:
		// Dropping a display event is better than blocking the run; the
		// final state is always reconciled by the UI.
	}
}

func (a *App) approve(request agent.ApprovalRequest) agent.Decision {
	if a.interactor == nil {
		return agent.DecisionDeny
	}
	return a.interactor.Approve(request)
}

func (a *App) ask(question string, options []string) (string, error) {
	if a.interactor == nil {
		return "", errors.New("the operator is not available for questions")
	}
	return a.interactor.Ask(question, options)
}

func (a *App) runSubagent(ctx context.Context, subType, prompt string) (string, error) {
	a.mu.Lock()
	client := a.client
	model := a.wireModel
	a.mu.Unlock()
	if client == nil {
		return "", errors.New("no provider client is available")
	}
	return agent.RunSubagent(ctx, a.env(), client, model, subType, prompt, agent.SubagentMaxSteps)
}

// RunTurn runs one operator turn, streaming events to the UI.
func (a *App) RunTurn(ctx context.Context, input string) error {
	return a.runTurn(ctx, input)
}

// runTurn is the single path a turn takes, whether it was typed in the TUI,
// sent over Telegram or passed to `termixgo run`. Sharing it keeps the model,
// budget, approval policy and tool wiring identical everywhere.
//
// The single-run slot is taken with TryLock rather than Lock: a second caller
// must be told the agent is busy instead of queueing behind a turn it cannot
// see, which is what let a Telegram prompt block the terminal indefinitely.
func (a *App) runTurn(ctx context.Context, input string) error {
	if !a.runMu.TryLock() {
		return ErrBusy
	}
	defer a.runMu.Unlock()

	a.mu.Lock()
	client := a.client
	model := a.wireModel
	window := a.model.Window()
	price := a.pricing
	costKnown := a.costKnown
	modelErr := a.modelErr
	if client == nil {
		a.mu.Unlock()
		if modelErr != nil {
			// Name the actual cause: a bare ErrNoModel read as "nothing is
			// configured" while /model was showing a configured id.
			return fmt.Errorf("%w: %v", ErrNoModel, modelErr)
		}
		return ErrNoModel
	}
	a.running = true
	a.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel = cancel
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		a.cancel = nil
		a.running = false
		a.mu.Unlock()
	}()

	// A best-effort snapshot before the turn, so a bad edit can be undone
	// with /rewind. It never fails the turn: outside git, or when git is
	// slow, the turn simply runs without a new checkpoint.
	agent.AutoCheckpoint(runCtx, a.workspace)

	cfg := a.Config()
	runner := &agent.Runner{
		Client:        client,
		Model:         model,
		Config:        cfg,
		Env:           a.env(),
		Tools:         a.tools,
		Policy:        a.Policy(),
		MaxSteps:      cfg.MaxSteps,
		Harness:       cfg.HarnessProfile,
		ContextBudget: agent.HistoryBudget(window),
		// An interactive turn continues past its step budget while it is still
		// making progress, so a small configured MaxSteps cannot pause real work
		// mid-task. A subagent leaves this unset and keeps a hard budget.
		TurnSegments:  agent.DefaultTurnSegments,
		Pricing:       price,
		CostKnown:     costKnown,
		CostBudgetUSD: cfg.CostBudgetUSD,
		Steer:         a.TakeSteer,
		Journal:       a.journal,
		ToolSearch:    cfg.ToolSearchEnabled,
	}
	session := a.currentSession()
	if err := runner.Run(runCtx, session, input); err != nil {
		return err
	}
	session.SetTodos(a.todos.Items())
	if a.Ephemeral() {
		return nil
	}
	return session.Save()
}

// RunPrompt runs one turn for the Telegram bridge, forwarding short progress
// lines and returning the final answer text.
func (a *App) RunPrompt(ctx context.Context, prompt string, progress func(string)) (string, error) {
	if progress != nil {
		progress("Working...")
		a.SetObserver(func(event agent.Event) {
			if line := telegramProgressLine(event); line != "" {
				progress(line)
			}
		})
		defer a.SetObserver(nil)
	}
	if err := a.runTurn(ctx, prompt); err != nil {
		return "", err
	}
	return a.currentSession().LastAssistantText(), nil
}

// telegramProgressLine renders one event as a short chat line.
func telegramProgressLine(event agent.Event) string {
	switch event.Kind {
	case agent.EventToolStart:
		return event.ToolLabel
	case agent.EventToolEnd:
		if !event.ToolOK {
			return event.ToolLabel + " (failed)"
		}
		return event.ToolLabel
	case agent.EventNotice:
		return event.Text
	case agent.EventError:
		if event.Err != nil {
			return event.Err.Error()
		}
	}
	return ""
}

// Status renders the short status block shared by /status and the bot.
func (a *App) Status() string {
	usage := a.Usage()
	done, total := a.todos.Progress()
	session := a.Session()
	trust := "untrusted"
	if a.Trusted() {
		trust = "trusted"
	}
	model := a.CurrentModel()
	lines := []string{
		fmt.Sprintf("workspace: %s (%s)", a.workspace, trust),
		fmt.Sprintf("model: %s (%s)", orNone(a.ModelLabel()), orNone(model.Provider)),
	}
	if plan, ok := model.Plan(); ok {
		lines = append(lines, fmt.Sprintf("billing: %s (%s, not dollars)", plan.Name, plan.CreditUnit))
	}
	lines = append(lines,
		fmt.Sprintf("approval: %s", a.Config().ApprovalMode),
		fmt.Sprintf("context: %s", a.contextUsage()),
		fmt.Sprintf("session: %s, %d turn(s)", session.ID(), session.Turns()),
		fmt.Sprintf("plan: %d/%d complete", done, total),
		fmt.Sprintf("tokens: %d in, %d out", usage.PromptTokens, usage.CompletionTokens),
	)
	if err := a.ModelError(); err != nil {
		lines = append(lines, "model problem: "+err.Error())
	}
	if a.botStatus != "" {
		lines = append(lines, "telegram: "+a.botStatus)
	} else {
		lines = append(lines, "telegram: off")
	}
	return strings.Join(lines, "\n")
}

// HasModel reports whether a usable model is configured.
func (a *App) HasModel() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.client != nil
}

// ModelError reports why the configured model has no usable client. It is nil
// when the model is ready. The label can name a model while this is set: the
// id is configured, but its key or endpoint is missing.
func (a *App) ModelError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.modelErr
}

// ContextUsage renders how much of the model's window the conversation is
// using, which is the number that explains an unexpected trim.
func (a *App) ContextUsage() string { return a.contextUsage() }

func (a *App) contextUsage() string {
	session := a.Session()
	return agent.HistoryHint(session.Messages(), agent.HistoryBudget(a.CurrentModel().Window()))
}

// NeedsSetup reports whether onboarding is required.
func (a *App) NeedsSetup() bool {
	return !a.HasModel()
}

// ReloadSkills refreshes the skill list.
func (a *App) ReloadSkills() {
	if skills, err := skill.Discover(a.workspace); err == nil {
		a.mu.Lock()
		a.skills = skills
		a.mu.Unlock()
	}
}

// Tools exposes the tool registry for /tools.
func (a *App) Tools() *agent.Registry { return a.tools }

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			set[trimmed] = true
		}
	}
	return set
}

// pricingFor resolves a model's price and whether it is known.
func (a *App) pricingFor(model provider.Model) (provider.Pricing, bool) {
	return provider.PricedFor(a.Config(), model)
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}
