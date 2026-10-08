package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

// TestHistoryBudgetIsNeverZero pins the wiring behind the stored-transcript
// bound. A zero budget would silently disable it, and the symptom would be a
// session file that grows for the life of the session with nothing in the code
// to point at.
func TestHistoryBudgetIsNeverZero(t *testing.T) {
	application := newTestApp(t)
	budget := application.historyBudget()
	if budget <= 0 {
		t.Fatalf("historyBudget = %d, want the bound to be active", budget)
	}
	// The number has to be the one the request itself is trimmed to, derived
	// from the active model's window, or the stored transcript would be bounded
	// by a different rule than the one the model works from.
	if want := agent.HistoryBudget(application.CurrentModel().Window()); budget != want {
		t.Errorf("historyBudget = %d, want the request budget %d", budget, want)
	}
}

// TestEnvSeedsTheAllowedTools keeps the subagent inheritance wired: a nested run
// reads the operator's answers from the environment it inherits, so a config
// that names an always-allowed tool has to reach Env.AlwaysAllowed.
func TestEnvSeedsTheAllowedTools(t *testing.T) {
	application := newTestApp(t)
	if got := application.env().AlwaysAllowed; got == nil {
		t.Errorf("Env.AlwaysAllowed is nil, want a map a subagent can inherit")
	}
	application.AllowTool("write_file")
	env := application.env()
	if !env.AlwaysAllowed["write_file"] {
		t.Errorf("Env.AlwaysAllowed = %v, want write_file after an allow-always answer", env.AlwaysAllowed)
	}
}
