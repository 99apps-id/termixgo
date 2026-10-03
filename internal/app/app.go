// Package app wires the pieces together: configuration, secrets, providers,
// skills, the agent runner and the Telegram companion. The terminal UI and
// the bot both drive this one object, so a prompt means the same thing
// wherever it is typed.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/audit"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/cron"
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
	// subagentClients caches one client per provider a subagent role maps to,
	// so a delegated role can run on a different model without rebuilding the
	// client (and its token refresher) on every spawn.
	subagentClients map[string]provider.Client
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
	// observer is the stream sink for the turn that holds the slot, and
	// observerClaim is the token that releasing it is restricted to.
	observer      func(agent.Event)
	observerClaim int64

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

	// cronStore is the scheduled-job list. It is opened lazily so a terminal
	// session with no scheduler still pays nothing for it.
	cronStore *cron.Store
	// scheduler runs scheduled jobs and the heartbeat. It is started by
	// `serve`, which is the long-lived process.
	scheduler       *cron.Scheduler
	schedulerCancel context.CancelFunc
	schedulerStatus string

	// mcp owns the connected MCP servers and the tools they contribute. It is
	// replaced rather than mutated on a reload, so a turn already in flight
	// keeps the registry it started with.
	mcp *mcp.Pool

	// journal records recurring tool failures so the agent can learn from them.
	journal *agent.ErrorJournal

	// audit is the metadata-only ledger of finished turns and tool calls. A
	// write failure never fails a turn, so it is best effort.
	audit *audit.Ledger

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
	if path, err := audit.DefaultPath(); err == nil {
		if ledger, err := audit.Open(path); err == nil {
			instance.audit = ledger
		}
	}
	// The manager outlives a turn, so its emitter is installed once here rather
	// than being handed a per-run environment.
	instance.processes.SetEmitter(instance.emit)
	instance.policy = &agent.ApprovalPolicy{
		Mode:           agent.ApprovalModeOrDefault(cfg),
		AlwaysAllowed:  stringSet(cfg.AlwaysAllowedTools),
		SessionAllowed: map[string]bool{},
		Memory:         instance.memory,
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
	// A server's environment can carry a token (GITHUB_PERSONAL_ACCESS_TOKEN).
	// It lives in the 0600 secret file, not config.json, which has no explicit
	// owner-only ACL on Windows; any pair already in the config is moved here
	// once.
	a.migrateMCPEnv()
	cfg := a.Config()
	enabled := make([]mcp.Options, 0, len(cfg.MCPServers))
	disabled := make([]mcp.Options, 0, len(cfg.MCPServers))
	for _, server := range cfg.MCPServers {
		options := mcp.Options{
			Name:    server.Name,
			Command: server.Command,
			Args:    server.Args,
			Env:     a.mcpEnvFor(server.Name),
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

// AddMCPServer saves an MCP server configuration. Its environment is a place
// for a token, so it goes to the secret file and is cleared from the config
// before the config is written.
func (a *App) AddMCPServer(server config.MCPServer) error {
	if len(server.Env) > 0 {
		env := a.mcpEnvFor(server.Name)
		for key, value := range server.Env {
			env[key] = value
		}
		if err := a.saveMCPEnv(server.Name, env); err != nil {
			return err
		}
		server.Env = nil
	}
	a.mu.Lock()
	a.cfg = a.cfg.WithMCPServer(server)
	cfg := a.cfg
	a.mu.Unlock()
	return config.Save(cfg)
}

// RemoveMCPServer deletes an MCP server by name.
func (a *App) RemoveMCPServer(name string) error {
	a.mu.Lock()
	newCfg, found := a.cfg.WithoutMCPServer(name)
	if !found {
		a.mu.Unlock()
		return fmt.Errorf("mcp server %q not found", name)
	}
	a.cfg = newCfg
	cfg := a.cfg
	a.mu.Unlock()
	return config.Save(cfg)
}

// SetMCPServerEnabled enables or disables an MCP server by name.
func (a *App) SetMCPServerEnabled(name string, enabled bool) error {
	a.mu.Lock()
	newCfg, found := a.cfg.SetMCPServerDisabled(name, !enabled)
	if !found {
		a.mu.Unlock()
		return fmt.Errorf("mcp server %q not found", name)
	}
	a.cfg = newCfg
	cfg := a.cfg
	a.mu.Unlock()
	return config.Save(cfg)
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
		if info.OAuth {
			a.modelErr = fmt.Errorf("%s needs a login; run 'termixgo login %s'", info.Label, info.ID)
			return a.modelErr
		}
		a.modelErr = fmt.Errorf("no API key for %s yet; run /setup", info.Label)
		return a.modelErr
	}
	client, err := provider.NewClient(info.ID, provider.BaseURLFor(a.cfg, info.ID), provider.ResolverFor(a.store))
	if err != nil {
		a.modelErr = err
		return err
	}
	if info.OAuth {
		if setter, ok := client.(interface{ SetForceKeyResolver(provider.KeyResolver) }); ok {
			setter.SetForceKeyResolver(provider.ForceResolverFor(a.store))
		}
		// A Codex request must name the ChatGPT account the token belongs to.
		if token, ok := provider.OAuthStore(a.store).Load(info.ID); ok {
			if setter, ok := client.(interface{ SetAccountID(string) }); ok {
				setter.SetAccountID(token.AccountID)
			}
		}
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

// SetObserver installs a single agent-event observer for one turn and returns
// the claim that releases it. It is how the Telegram bridge follows a run
// without competing with the terminal UI for the event channel.
//
// The slot belongs to whoever claimed it, and a claim is refused rather than
// honoured out of turn. A caller that finds it occupied keeps running and
// simply streams nothing, which is the right outcome: the only such caller is a
// second surface whose turn is about to be refused as busy, because this slot
// guards the same single run that runMu does. Releasing is restricted to the
// holder for the same reason. Before that, a second inbound message installed
// its own observer, lost the run race, and cleared the slot on the way out, so
// the turn that was still streaming lost its sink: the chat sat on
// "Working..." and the prompt returned an empty answer.
//
// A claim of 0 means nothing was installed, and ClearObserver ignores it.
// Passing nil is a forced clear rather than a claim: it exists so a test can
// switch the slot off without holding one, and no run path should use it, since
// that is exactly the unowned release described above.
func (a *App) SetObserver(observer func(agent.Event)) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if observer == nil {
		a.observer = nil
		a.observerClaim = 0
		return 0
	}
	if a.observer != nil {
		return 0
	}
	a.observerClaim++
	a.observer = observer
	return a.observerClaim
}

// ClearObserver releases a claim on the observer slot. Only the holder of that
// claim can release it, and a claim of 0 releases nothing.
func (a *App) ClearObserver(claim int64) {
	if claim == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.observerClaim != claim {
		return
	}
	a.observer = nil
	a.observerClaim = 0
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
	a.replacePolicy(agent.ApprovalModeOrDefault(a.cfg))
	a.pricing, a.costKnown = provider.PricedFor(a.cfg, a.model)
	cfg := a.cfg
	a.mu.Unlock()
	return config.Save(cfg)
}

// replacePolicy installs a policy with a new mode while keeping the parts that
// are not about the mode. The caller holds a.mu.
//
// AlwaysAllowed and SessionAllowed survive a policy replacement, and so does
// Memory: it is where the loop records "the operator allowed this tool", so
// dropping it silently stopped the agent learning approval decisions after any
// config write.
func (a *App) replacePolicy(mode agent.ApprovalMode) {
	previous := a.policy
	policy := &agent.ApprovalPolicy{Mode: mode, Memory: a.memory}
	if previous != nil {
		policy.AlwaysAllowed = previous.AlwaysAllowed
		policy.SessionAllowed = previous.SessionAllowed
	}
	a.policy = policy
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
	a.replacePolicy(agent.ApprovalMode(mode))
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

// mcpEnvPrefix namespaces a server's environment inside the secret store.
const mcpEnvPrefix = "mcp-env:"

// mcpEnvFor returns a server's environment from the secret store.
func (a *App) mcpEnvFor(name string) map[string]string {
	env := map[string]string{}
	key := mcpEnvPrefix + strings.ToLower(strings.TrimSpace(name))
	if a.store == nil {
		return env
	}
	raw := strings.TrimSpace(a.store.Get(key))
	if raw == "" {
		return env
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil || env == nil {
		return map[string]string{}
	}
	return env
}

// saveMCPEnv stores a server's environment in the 0600 secret file.
func (a *App) saveMCPEnv(name string, env map[string]string) error {
	if a.store == nil {
		return errors.New("no secret store is available")
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return a.store.Set(mcpEnvPrefix+strings.ToLower(strings.TrimSpace(name)), string(data))
}

// migrateMCPEnv moves any environment a config.json still carries into the
// secret store and removes it from the config, so a token that was written to
// the world-readable file by an older version ends up owner-only.
func (a *App) migrateMCPEnv() {
	migrated := map[string]bool{}
	for _, server := range a.Config().MCPServers {
		if len(server.Env) == 0 {
			continue
		}
		env := a.mcpEnvFor(server.Name)
		for key, value := range server.Env {
			env[key] = value
		}
		if err := a.saveMCPEnv(server.Name, env); err != nil {
			continue
		}
		migrated[strings.ToLower(strings.TrimSpace(server.Name))] = true
	}
	if len(migrated) == 0 {
		return
	}
	_ = a.UpdateConfig(func(c *config.Config) {
		for index := range c.MCPServers {
			if migrated[strings.ToLower(strings.TrimSpace(c.MCPServers[index].Name))] {
				c.MCPServers[index].Env = nil
			}
		}
	})
}

// isolatedEnv returns a per-run environment for a scheduled job. It shares the
// model, tools, secrets and search, but keeps a throwaway todo store and an
// emitter that does not fold usage into the operator's live session, so a
// background turn cannot rewrite the operator's plan or its spend.
func (a *App) isolatedEnv() *agent.Env {
	base := a.env()
	base.Todos = agent.NewTodoStore()
	base.Emit = func(event agent.Event) {
		a.emitInto(event, false)
	}
	return base
}

// Processes exposes the background process manager.
func (a *App) Processes() *agent.ProcessManager { return a.processes }

// Shutdown stops everything the app started. The command calls it on exit so a
// dev server does not outlive the terminal that started it.
func (a *App) Shutdown() {
	a.StopScheduler()
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
	a.emitInto(event, true)
}

// emitInto is the shared emit path. A scheduled run calls it with foldUsage
// false so its usage and spend never land on the operator's live session or
// app total; the throwaway session still accounts for itself.
func (a *App) emitInto(event agent.Event, foldUsage bool) {
	a.recordAudit(event)
	if event.Kind == agent.EventProcessEnd {
		// A worker announced completion; forward it to chat so a 24/7
		// operator hears about it without watching the terminal. Delivery
		// runs off the emit path so a slow send never blocks a turn.
		go func(text string) {
			_ = a.Notify(context.Background(), text)
		}(event.Text)
	}
	if foldUsage && event.Kind == agent.EventUsage {
		a.AddUsage(event.Usage)
	}
	if event.Kind == agent.EventToolEnd {
		a.recordToolResult(event.ToolName, event.ToolOK)
	}
	if foldUsage {
		switch event.Kind {
		case agent.EventUsage, agent.EventTurnEnd:
			// Every usage event carries the run's running total, so assigning is
			// correct and also repairs the value after a resumed session.
			a.mu.Lock()
			a.costUSD = event.CostUSD
			a.costKnown = event.CostKnown
			a.mu.Unlock()
		}
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

// recordAudit appends a metadata-only line for a finished tool call or turn.
// The ledger never carries a prompt, a result or a secret.
func (a *App) recordAudit(event agent.Event) {
	ledger := a.audit
	if ledger == nil {
		return
	}
	switch event.Kind {
	case agent.EventToolEnd:
		_ = ledger.Record(audit.Entry{
			Workspace: a.workspace,
			Kind:      "tool",
			Name:      event.ToolName,
			OK:        event.ToolOK,
			Millis:    event.ToolMillis,
		})
	case agent.EventTurnEnd:
		_ = ledger.Record(audit.Entry{
			Workspace:    a.workspace,
			Kind:         "turn",
			OK:           event.StopReason == "stop",
			StopReason:   event.StopReason,
			InputTokens:  event.Usage.PromptTokens,
			OutputTokens: event.Usage.CompletionTokens,
		})
	}
}

// AuditTail returns the newest audit entries, oldest first.
func (a *App) AuditTail(limit int) ([]audit.Entry, error) {
	a.mu.Lock()
	ledger := a.audit
	a.mu.Unlock()
	if ledger == nil {
		return nil, nil
	}
	return ledger.Tail(limit)
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
	model := a.model
	primary := strings.TrimSpace(a.cfg.SubagentModels[strings.ToLower(strings.TrimSpace(subType))])
	if primary == "" && strings.ToLower(strings.TrimSpace(subType)) == "image" {
		primary = strings.TrimSpace(a.cfg.ImageModel)
	}
	fallbacks := append([]string(nil), a.cfg.SubagentFallbacks...)
	a.mu.Unlock()
	if client == nil {
		return "", errors.New("no provider client is available")
	}

	// An explicit fallback chain is authoritative: a subagent stays on the
	// providers the operator listed and never silently lands on another one.
	if len(fallbacks) > 0 {
		var lastErr error
		seen := map[string]bool{}
		for _, query := range subagentChain(primary, fallbacks) {
			roleClient, roleModel, err := a.subagentClient(query)
			if err != nil {
				lastErr = err
				continue
			}
			key := roleModel.Provider + ":" + roleModel.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			report, runErr := agent.RunSubagent(ctx, a.env(), roleClient, roleModel, subType, prompt, agent.SubagentMaxSteps)
			if runErr == nil {
				return report, nil
			}
			if !subagentFallback(runErr) {
				return "", runErr
			}
			lastErr = runErr
		}
		if lastErr == nil {
			lastErr = errors.New("no subagent model is available")
		}
		return "", lastErr
	}

	// No chain: the role model, then the active model.
	if primary != "" {
		if roleClient, roleModel, err := a.subagentClient(primary); err == nil && roleModel.ID != model.ID {
			report, runErr := agent.RunSubagent(ctx, a.env(), roleClient, roleModel, subType, prompt, agent.SubagentMaxSteps)
			if runErr == nil {
				return report, nil
			}
			if !subagentFallback(runErr) {
				return "", runErr
			}
		}
	}
	return agent.RunSubagent(ctx, a.env(), client, model, subType, prompt, agent.SubagentMaxSteps)
}

// subagentChain is the role's model followed by the fallbacks, blank entries
// dropped, so the caller can iterate one ordered list.
func subagentChain(primary string, fallbacks []string) []string {
	chain := make([]string, 0, len(fallbacks)+1)
	if strings.TrimSpace(primary) != "" {
		chain = append(chain, primary)
	}
	for _, entry := range fallbacks {
		if strings.TrimSpace(entry) != "" {
			chain = append(chain, entry)
		}
	}
	return chain
}

// subagentClient resolves a subagent's configured model and returns a cached
// client for its provider.
func (a *App) subagentClient(query string) (provider.Client, provider.Model, error) {
	model, err := resolveSubagentModel(a.cfg, query)
	if err != nil {
		return nil, provider.Model{}, err
	}
	info, ok := provider.ByID(model.Provider)
	if !ok {
		return nil, provider.Model{}, fmt.Errorf("unknown provider %q", model.Provider)
	}
	if info.NeedsKey && provider.KeySource(a.store, info.ID) == "" {
		return nil, provider.Model{}, fmt.Errorf("%s has no credential", info.ID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if cached, ok := a.subagentClients[info.ID]; ok {
		return cached, model, nil
	}
	client, err := provider.NewClient(info.ID, provider.BaseURLFor(a.cfg, info.ID), provider.ResolverFor(a.store))
	if err != nil {
		return nil, provider.Model{}, err
	}
	if info.OAuth {
		if setter, ok := client.(interface {
			SetForceKeyResolver(provider.KeyResolver)
		}); ok {
			setter.SetForceKeyResolver(provider.ForceResolverFor(a.store))
		}
		if token, ok := provider.OAuthStore(a.store).Load(info.ID); ok {
			if setter, ok := client.(interface{ SetAccountID(string) }); ok {
				setter.SetAccountID(token.AccountID)
			}
		}
	}
	if a.subagentClients == nil {
		a.subagentClients = map[string]provider.Client{}
	}
	a.subagentClients[info.ID] = client
	return client, model, nil
}

// resolveSubagentModel turns "provider:model" or "provider/model" or a
// catalogue id into a model.
func resolveSubagentModel(cfg config.Config, query string) (provider.Model, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return provider.Model{}, errors.New("the subagent model is empty")
	}
	if index := strings.Index(trimmed, ":"); index > 0 {
		if _, ok := provider.ByID(trimmed[:index]); ok {
			if model, ok := provider.ModelFromQuery(trimmed); ok {
				return model, nil
			}
		}
	}
	if model, ok := provider.ModelFromQuery(trimmed); ok {
		return model, nil
	}
	if index := strings.Index(trimmed, "/"); index > 0 {
		if _, ok := provider.ByID(trimmed[:index]); ok {
			wire := strings.TrimLeft(trimmed[index+1:], "/")
			return provider.Model{ID: trimmed, Provider: trimmed[:index], Label: wire, APIID: wire}, nil
		}
	}
	return provider.Model{}, fmt.Errorf("unknown subagent model %q", query)
}

// subagentFallback reports whether a delegated run failed in a way that another
// model can cover: a spent quota or a provider that is briefly unavailable.
func subagentFallback(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, needle := range []string{
		"quota", "rate limit", "too many requests", "429", "402",
		"exhausted", "insufficient", "credit", "billing", "payment",
		"overloaded", "unavailable", "502", "503", "504",
	} {
		if strings.Contains(message, needle) {
			return true
		}
	}
	return false
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
	return a.runTurnWithImages(ctx, input, nil)
}

// runTurnWithImages is runTurn with images attached to the first user message,
// which is how an uploaded image reaches a vision model.
func (a *App) runTurnWithImages(ctx context.Context, input string, images []provider.Image) error {
	return a.runOn(ctx, input, images, nil)
}

// runOn is the single run path. A nil isolated session uses the live one; a
// session passed in is a scheduled run that must not touch the operator's
// conversation, so it is never saved and its todos are not published.
func (a *App) runOn(ctx context.Context, input string, images []provider.Image, isolated *agent.Session) error {
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
	runEnv := a.env()
	if isolated != nil {
		runEnv = a.isolatedEnv()
	}
	runner := &agent.Runner{
		Client: client,
		Model:  model,
		Config: cfg,
		Env:    runEnv,
		Tools:  a.tools,
		Policy: a.Policy(),
		// cfg.MaxSteps is the whole per-turn ceiling, so an interactive turn
		// pauses once, at the number the operator configured.
		MaxSteps:      cfg.MaxSteps,
		Harness:       cfg.HarnessProfile,
		ContextBudget: agent.HistoryBudget(window),
		Pricing:       price,
		CostKnown:     costKnown,
		CostBudgetUSD: cfg.CostBudgetUSD,
		Steer:         a.TakeSteer,
		Journal:       a.journal,
		ToolSearch:    cfg.ToolSearchEnabled,
		TurnImages:    images,
	}
	session := isolated
	if session == nil {
		session = a.currentSession()
	}
	if err := runner.Run(runCtx, session, input); err != nil {
		return err
	}
	if isolated == nil {
		session.SetTodos(a.todos.Items())
	}
	if isolated != nil || a.Ephemeral() {
		return nil
	}
	return session.Save()
}

// GitDiff returns the current git diff of the workspace.
func (a *App) GitDiff(ctx context.Context) (string, error) {
	env := a.env()
	cmd := exec.CommandContext(ctx, "git", "diff")
	cmd.Dir = env.Workspace
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// RunPrompt runs one turn for the Telegram bridge, forwarding short progress
// lines and returning the final answer text.
func (a *App) RunPrompt(ctx context.Context, prompt string, progress func(string)) (string, error) {
	notice := ""
	if progress != nil {
		// The opening line is part of the contract: the caller shows it at once,
		// before the first event arrives.
		progress("Working...")
		claim := a.SetObserver(func(event agent.Event) {
			switch {
			case event.Kind == agent.EventNotice && strings.TrimSpace(event.Text) != "":
				notice = event.Text
			case event.Kind == agent.EventError && event.Err != nil:
				notice = event.Err.Error()
			}
			if line := telegramProgressLine(event); line != "" {
				progress(line)
			}
		})
		defer a.ClearObserver(claim)
	}
	session := a.currentSession()
	mark := session.MessageCount()
	if err := a.runTurn(ctx, prompt); err != nil {
		return "", err
	}
	// Report only what this turn produced. Falling back to the session's last
	// answer would send an earlier turn's reply again when the model said
	// nothing, which reads as the agent repeating itself.
	if answer := strings.TrimSpace(session.LastAssistantTextSince(mark)); answer != "" {
		return answer, nil
	}
	return notice, nil
}

// RunPromptWithImage runs one turn with an image attached to the prompt, for a
// photo the operator sent. mediaType is a MIME type and data is base64.
func (a *App) RunPromptWithImage(ctx context.Context, prompt, mediaType, data string, progress func(string)) (string, error) {
	notice := ""
	if progress != nil {
		progress("Working...")
		claim := a.SetObserver(func(event agent.Event) {
			switch {
			case event.Kind == agent.EventNotice && strings.TrimSpace(event.Text) != "":
				notice = event.Text
			case event.Kind == agent.EventError && event.Err != nil:
				notice = event.Err.Error()
			}
			if line := telegramProgressLine(event); line != "" {
				progress(line)
			}
		})
		defer a.ClearObserver(claim)
	}
	images := []provider.Image{{MediaType: mediaType, Data: data}}
	session := a.currentSession()
	mark := session.MessageCount()
	if err := a.runTurnWithImages(ctx, prompt, images); err != nil {
		return "", err
	}
	if answer := strings.TrimSpace(session.LastAssistantTextSince(mark)); answer != "" {
		return answer, nil
	}
	return notice, nil
}

// RunVoicePrompt runs a turn initiated from a voice note. If the active model
// errors and a voice fallback model is configured, it retries with the voice model.
func (a *App) RunVoicePrompt(ctx context.Context, prompt string, progress func(string)) (string, error) {
	answer, err := a.RunPrompt(ctx, prompt, progress)
	if err == nil {
		return answer, nil
	}

	a.mu.Lock()
	voiceModelID := strings.TrimSpace(a.cfg.VoiceModel)
	maxSteps := a.cfg.MaxSteps
	a.mu.Unlock()

	if voiceModelID == "" {
		return "", err
	}

	if progress != nil {
		progress(fmt.Sprintf("Falling back to voice model (%s)...", voiceModelID))
	}

	roleClient, roleModel, clientErr := a.subagentClient(voiceModelID)
	if clientErr != nil {
		return "", fmt.Errorf("active turn failed (%v), and voice fallback model error: %w", err, clientErr)
	}

	report, subErr := agent.RunSubagent(ctx, a.env(), roleClient, roleModel, string(agent.SubagentGeneral), prompt, maxSteps)
	if subErr != nil {
		return "", fmt.Errorf("active turn failed (%v), and voice fallback turn error: %w", err, subErr)
	}

	session := a.currentSession()
	session.AddUser(prompt)
	session.AddAssistant(report, "", nil)
	_ = session.Save()

	return report, nil
}

// VoiceModel returns the configured voice model.
func (a *App) VoiceModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.VoiceModel
}

// SetVoiceModel sets and saves the voice model.
func (a *App) SetVoiceModel(model string) error {
	a.mu.Lock()
	a.cfg.VoiceModel = strings.TrimSpace(model)
	cfg := a.cfg
	a.mu.Unlock()
	return config.Save(cfg)
}

// ImageModel returns the configured image creation model.
func (a *App) ImageModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.ImageModel
}

// SetImageModel sets and saves the image creation model and updates the image subagent role.
func (a *App) SetImageModel(model string) error {
	a.mu.Lock()
	trimmed := strings.TrimSpace(model)
	a.cfg.ImageModel = trimmed
	if a.cfg.SubagentModels == nil {
		a.cfg.SubagentModels = map[string]string{}
	}
	if trimmed != "" {
		a.cfg.SubagentModels["image"] = trimmed
	} else {
		delete(a.cfg.SubagentModels, "image")
	}
	cfg := a.cfg
	a.mu.Unlock()
	return config.Save(cfg)
}

// ActiveProviders lists the providers the operator can use right now: the
// current provider, local providers that need no key, and providers with a
// stored key or login.
func (a *App) ActiveProviders() []provider.Provider {
	a.mu.Lock()
	current := a.model.Provider
	store := a.store
	a.mu.Unlock()

	var active []provider.Provider
	for _, info := range provider.Providers() {
		if len(provider.ModelsFor(info.ID)) == 0 {
			continue
		}
		if info.ID != current && info.NeedsKey && provider.KeySource(store, info.ID) == "" {
			continue
		}
		active = append(active, info)
	}
	return active
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
