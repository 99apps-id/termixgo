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

// maxInlineTools is the largest schema set a turn sends inline. OpenAI
// documents a 128-function limit for function calling, and a larger set makes
// every step expensive; a reserve-capacity model starts answering with no
// output at all around 150 tools. Above the cap the turn uses tool search: the
// core set stays visible and find_tools loads the rest on demand. A large MCP
// installation is the usual way to cross it, since one server can contribute
// hundreds of tools.
const maxInlineTools = 128

// shouldDeferTools reports whether the registry is large enough that the turn
// should send the core set plus find_tools instead of every schema.
func shouldDeferTools(count int) bool { return count > maxInlineTools }

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
