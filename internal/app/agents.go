package app

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/agenthome"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/search"
	"github.com/99apps-id/termixgo/internal/skill"
)

// agentContext is the per-workspace state one agent's turns run against:
// its own memory, error journal, search index, skills and trust answer.
//
// Two agents never share these, which is the point of a per-agent workspace:
// a fact one agent remembered or a file one agent indexed must not show up in
// the other's prompt or search results.
type agentContext struct {
	name      string
	home      string
	workspace string
	memory    *agent.Memory
	journal   *agent.ErrorJournal
	search    *search.Store
	skills    []skill.Skill
	trusted   bool
}

// ActiveAgent is the persona the terminal session talks to.
func (a *App) ActiveAgent() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.activeAgent
}

// AgentNames lists the configured agents, the default first and the rest
// alphabetically, which is the order the picker and /agent present them in.
func (a *App) AgentNames() []string {
	cfg := a.Config()
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		if name == config.FinalAgentName {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return append([]string{config.FinalAgentName}, names...)
}

// AddWhatsAppAccount registers one WhatsApp account from the onboarding
// wizard, creating the agent home when it is missing so the account has a
// workspace and a persona before the QR login. The account starts disabled:
// login is what enables it.
func (a *App) AddWhatsAppAccount(name string) error {
	trimmed := strings.TrimSpace(name)
	if !config.ValidAgentName(trimmed) {
		return fmt.Errorf("invalid account name %q", trimmed)
	}
	cfg := a.Config()
	if _, exists := cfg.WhatsAppAccount(trimmed); exists {
		return nil
	}
	if !cfg.HasAgent(trimmed) {
		paths, err := agenthome.Ensure(trimmed, "", "assistant")
		if err != nil {
			return err
		}
		if err := a.UpdateConfig(func(c *config.Config) {
			*c = c.Trust(paths.Workspace).WithAgent(trimmed, config.Agent{})
		}); err != nil {
			return err
		}
	}
	return a.UpdateConfig(func(c *config.Config) {
		*c = c.WithWhatsAppAccount(trimmed, config.ChannelAccount{
			Agent:       trimmed,
			DMPolicy:    config.DMPolicyAllowlist,
			GroupPolicy: config.GroupPolicyMention,
		})
	})
}

// AgentWorkspace is where one agent reads and writes.
func (a *App) AgentWorkspace(name string) string {
	return a.agentContextFor(name).workspace
}

// AgentHome is the home directory of one agent.
func (a *App) AgentHome(name string) string {
	return a.agentContextFor(name).home
}

// SetActiveAgent switches the terminal session to another persona. The live
// conversation of the agent being left is remembered in process, and the
// target's last saved conversation is resumed; a first switch starts one.
func (a *App) SetActiveAgent(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("an agent name is required")
	}
	a.mu.Lock()
	cfg := a.cfg
	current := a.activeAgent
	live := a.session
	a.mu.Unlock()

	if !cfg.HasAgent(trimmed) {
		return fmt.Errorf("unknown agent %q; create it with 'termixgo agent add %s'", trimmed, trimmed)
	}
	if trimmed == current {
		return nil
	}

	a.mu.Lock()
	if live != nil {
		a.agentSessions[current] = live
	}
	a.mu.Unlock()

	next := a.agentSessionFor(trimmed)
	a.mu.Lock()
	a.activeAgent = trimmed
	a.session = next
	a.mu.Unlock()
	a.todos.Set(next.Todos())

	// The active agent is configuration so a restart resumes the same persona;
	// a failed write costs the preference, not the switch.
	_ = a.UpdateConfig(func(c *config.Config) { c.ActiveAgent = trimmed })
	return nil
}

// agentContextFor returns the (lazily built) context of one agent. The
// default agent's context is seeded in New so it can share state with the
// legacy single-workspace fields.
func (a *App) agentContextFor(name string) *agentContext {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		trimmed = config.FinalAgentName
	}
	a.mu.Lock()
	if ctx, ok := a.agentCtx[trimmed]; ok {
		a.mu.Unlock()
		return ctx
	}
	cfg := a.cfg
	launch := a.workspace
	a.mu.Unlock()

	built := buildAgentContext(trimmed, cfg, launch)

	a.mu.Lock()
	if existing, ok := a.agentCtx[trimmed]; ok {
		// Another caller built it first. Keep one context and close the
		// duplicate index so two connections never sit on the same database.
		if built.search != nil {
			_ = built.search.Close()
		}
		a.mu.Unlock()
		return existing
	}
	a.agentCtx[trimmed] = built
	a.mu.Unlock()
	return built
}

// buildAgentContext opens the per-workspace stores for one agent. Failures
// degrade rather than abort: a missing search index only costs the substring
// fallback, and a missing journal is the shape a fresh workspace starts in.
func buildAgentContext(name string, cfg config.Config, launchWorkspace string) *agentContext {
	paths, err := agenthome.Resolve(name, cfg.Agent(name).Workspace)
	workspace := launchWorkspace
	if err == nil {
		workspace = paths.Workspace
	}
	ctx := &agentContext{
		name:      name,
		workspace: workspace,
		memory:    agent.NewMemory(workspace),
		trusted:   cfg.IsTrusted(workspace),
	}
	if err == nil {
		ctx.home = paths.Home
		ctx.memory.SetAgentHome(paths.Home)
	}
	if journal, err := agent.NewErrorJournal(workspace); err == nil {
		ctx.journal = journal
	}
	if store, err := search.OpenIn(workspace, searchStateDir(workspace)); err == nil {
		ctx.search = store
		agent.IndexLearnedContent(store, ctx.memory, ctx.journal)
		agent.IndexAgentDocs(store, ctx.home)
	}
	if skills, err := skill.Discover(workspace); err == nil {
		ctx.skills = skills
	}
	return ctx
}

// searchStateDir is where the full-text index lives for a workspace, the same
// directory the single-workspace build used.
func searchStateDir(workspace string) string {
	return filepath.Join(workspace, ".termixgo")
}

// agentSessionFor returns the live conversation for one agent: the one this
// process already holds, else the newest saved one, else a fresh session.
func (a *App) agentSessionFor(name string) *agent.Session {
	a.mu.Lock()
	if session, ok := a.agentSessions[name]; ok {
		a.mu.Unlock()
		return session
	}
	a.mu.Unlock()

	workspace := a.agentContextFor(name).workspace
	dir, dirErr := agenthome.SessionsDirFor(name)
	if dirErr == nil {
		if summary, ok, err := agent.LatestSessionFor(name, dir); err == nil && ok {
			if loaded, err := agent.LoadSessionIn(dir, summary.ID); err == nil {
				loaded.SetAgent(name)
				return loaded
			}
		}
	}
	session := agent.NewSessionFor(name, workspace, a.CurrentModel().ID)
	if dirErr == nil {
		session.SetStoreDir(dir)
	}
	return session
}

// newSessionForAgent starts a fresh conversation for one agent and pins it to
// that agent's sessions directory.
func (a *App) newSessionForAgent(name string) *agent.Session {
	workspace := a.agentContextFor(name).workspace
	session := agent.NewSessionFor(name, workspace, a.CurrentModel().ID)
	if dir, err := agenthome.SessionsDirFor(name); err == nil {
		session.SetStoreDir(dir)
	}
	return session
}

// agentModel resolves the model one agent's turns run on: its configured
// override when it has one, else the active model. A broken override falls
// back to the active model rather than failing the turn. The wire id is what
// the provider is sent.
func (a *App) agentModel(name string) (provider.Client, provider.Model, string) {
	a.mu.Lock()
	client := a.client
	active := a.model
	wire := a.wireModel
	a.mu.Unlock()

	override := strings.TrimSpace(a.Config().Agent(name).Model)
	if override == "" || override == active.ID {
		return client, active, wire
	}
	overrideClient, overrideModel, err := a.subagentClient(override)
	if err != nil {
		return client, active, wire
	}
	return overrideClient, overrideModel, provider.WireModel(a.Config(), overrideModel)
}

// defaultAgentWorkspace resolves where the default agent works: the launch
// directory unless the operator configured a workspace for it.
func defaultAgentWorkspace(cfg config.Config, launch string) string {
	if workspace := strings.TrimSpace(cfg.Agent(config.FinalAgentName).Workspace); workspace != "" {
		return config.CleanFolder(workspace)
	}
	return launch
}
