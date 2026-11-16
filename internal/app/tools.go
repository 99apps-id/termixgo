package app

import (
	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// buildDefaultTools is the built-in registry as the configuration allows it.
// Desktop control is opt-in, so the machine-driving tools are absent until the
// operator enables them: a tool the model cannot see is a tool it cannot ask
// for.
func buildDefaultTools(cfg config.Config) *agent.Registry {
	registry := agent.DefaultRegistry()
	if !cfg.DesktopControl {
		registry = registry.Without(agent.DesktopToolNames...)
	}
	return registry
}

// maxInlineTools is the shared cost bound from the agent package: above it
// every step resends and rebills the whole catalogue, so the turn uses tool
// search instead. The core set stays visible and find_tools loads the rest
// on demand. It sits well under the provider hard limits (OpenAI documents
// 128 functions) because cost, not refusal, is what binds first.
const maxInlineTools = agent.MaxInlineTools

// shouldDeferTools reports whether the registry is large enough that the turn
// should send the core set plus find_tools instead of every schema.
func shouldDeferTools(count int) bool { return agent.ShouldDeferTools(count) }

// SetDesktopControl turns the local desktop tools on or off and saves the
// setting. The live registry changes with it, so /desktop on takes effect on
// the next turn without a restart.
func (a *App) SetDesktopControl(on bool) error {
	a.mu.Lock()
	a.cfg.DesktopControl = on
	if on {
		a.tools = a.tools.With(agent.DesktopTools()...)
	} else {
		a.tools = a.tools.Without(agent.DesktopToolNames...)
	}
	cfg := a.cfg
	a.mu.Unlock()
	return config.Save(cfg)
}
