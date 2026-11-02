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
