package agent

import (
	"strings"
	"testing"
)

// TestHarnessDefaultsToCritical pins the full-agentic default: an empty id
// resolves to the critical profile, so every workspace gets evidence-first
// guidance unless configured otherwise.
func TestHarnessDefaultsToCritical(t *testing.T) {
	profile := GetHarnessProfile("")
	if profile.ID != DefaultHarnessProfile {
		t.Errorf("default profile = %q, want %q", profile.ID, DefaultHarnessProfile)
	}
	if strings.TrimSpace(profile.PromptPrelude) == "" {
		t.Errorf("the critical profile must carry a prelude")
	}
}

// TestHarnessBudgetNeverCollapsesToZero keeps a misconfigured budget from
// disabling the loop bound the anti-stuck guards rely on.
func TestHarnessBudgetNeverCollapsesToZero(t *testing.T) {
	profile := GetHarnessProfile("shorter_loop")
	if got := ApplyHarnessToBudget(0, profile); got < 1 {
		t.Errorf("budget = %d, want at least 1", got)
	}
	autonomous := GetHarnessProfile("autonomous")
	if got := ApplyHarnessToBudget(25, autonomous); got <= 25 {
		t.Errorf("autonomous budget = %d, want above the base", got)
	}
}

// TestDefaultStepBudgetIsGenerous pins the raised budget. The budget bounds one
// turn rather than the task, so an unset value must resolve to the generous
// default instead of a small number that pauses long work early.
func TestDefaultStepBudgetIsGenerous(t *testing.T) {
	if DefaultStepBudget != 100 {
		t.Errorf("DefaultStepBudget = %d, want 100", DefaultStepBudget)
	}
	profile := GetHarnessProfile(DefaultHarnessProfile)
	if got := ApplyHarnessToBudget(0, profile); got < DefaultStepBudget {
		t.Errorf("an unset budget resolved to %d, want at least %d", got, DefaultStepBudget)
	}
	// The autonomous profile must not cap below the default either.
	autonomous := GetHarnessProfile("autonomous")
	if got := ApplyHarnessToBudget(DefaultStepBudget, autonomous); got < DefaultStepBudget {
		t.Errorf("autonomous capped the default budget to %d", got)
	}
}

// TestHarnessIDsLeadWithTheDefault keeps the command surfaces consistent: the
// first id an operator sees is the one an empty config already uses.
func TestHarnessIDsLeadWithTheDefault(t *testing.T) {
	ids := HarnessProfileIDs()
	if len(ids) == 0 {
		t.Fatal("the harness list is empty")
	}
	if ids[0] != DefaultHarnessProfile {
		t.Errorf("first harness = %q, want the default %q", ids[0], DefaultHarnessProfile)
	}
	for _, needed := range []string{"critical", "autonomous", "verify_before_finish", "no_todo"} {
		found := false
		for _, id := range ids {
			if id == needed {
				found = true
			}
		}
		if !found {
			t.Errorf("harness %q is missing from the list", needed)
		}
	}
}

// TestRegistryForProfileKeepsTheAllowlist is the trust rule: a profile changes
// order and visibility, and a profile that names nothing changes nothing.
func TestRegistryForProfileKeepsTheAllowlist(t *testing.T) {
	base := DefaultRegistry()
	all := len(base.Tools())
	if all == 0 {
		t.Fatal("the default registry is empty")
	}

	// The critical default only prioritises, so nothing may disappear.
	critical := GetHarnessProfile("")
	filtered := RegistryForProfile(base, critical)
	if got := len(filtered.Tools()); got != all {
		t.Errorf("critical kept %d tools of %d; a profile that hides nothing must drop nothing", got, all)
	}
	if _, ok := filtered.Lookup("read_file"); !ok {
		t.Errorf("read_file vanished under the critical profile")
	}
	if filtered.Tools()[0].Name() != "read_file" {
		t.Errorf("first tool = %q, want the prioritised read_file", filtered.Tools()[0].Name())
	}

	// A profile that does name them does hide them, which is the operator's
	// explicit choice rather than a sandbox.
	noTodo := GetHarnessProfile("no_todo")
	trimmed := RegistryForProfile(base, noTodo)
	if _, ok := trimmed.Lookup("todo_write"); ok {
		t.Errorf("no_todo should hide todo_write")
	}
	if _, ok := trimmed.Lookup("read_file"); !ok {
		t.Errorf("no_todo must not hide anything else")
	}
	// The shared registry is untouched, so the choice cannot leak across turns.
	if _, ok := base.Lookup("todo_write"); !ok {
		t.Errorf("RegistryForProfile mutated the registry it was given")
	}
}
