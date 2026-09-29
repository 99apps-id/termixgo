package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

// runHarness sends the /harness command and returns the resulting model.
func runHarness(t *testing.T, model *Model, args string) *Model {
	t.Helper()
	next, _ := model.runSlash("harness", args)
	updated, ok := next.(*Model)
	if !ok {
		t.Fatalf("runSlash returned %T, want *Model", next)
	}
	return updated
}

// newestBlock reads the most recent transcript block.
func newestBlock(t *testing.T, model *Model) (blockKind, string) {
	t.Helper()
	if len(model.blocks) == 0 {
		t.Fatalf("the transcript is empty")
	}
	last := model.blocks[len(model.blocks)-1]
	return last.kind, last.text
}

// TestSlashHarnessListsAndNamesTheDefault pins the discovery path: an operator
// who types /harness with no argument sees every profile and which one is on.
func TestSlashHarnessListsAndNamesTheDefault(t *testing.T) {
	shown := runHarness(t, chatModel(t), "")
	_, text := newestBlock(t, shown)

	if !strings.Contains(text, agent.DefaultHarnessProfile) {
		t.Errorf("the listing should name the active profile:\n%s", text)
	}
	for _, profile := range agent.HarnessProfiles() {
		if !strings.Contains(text, profile.ID) {
			t.Errorf("the listing is missing %s:\n%s", profile.ID, text)
		}
	}
	if !strings.Contains(text, "> "+agent.DefaultHarnessProfile) {
		t.Errorf("the active profile should be marked:\n%s", text)
	}
}

// TestSlashHarnessSelectsRejectsAndReports covers the whole command: a valid id
// sticks, an invalid one is an error, and the answer says what happened.
func TestSlashHarnessSelectsRejectsAndReports(t *testing.T) {
	model := chatModel(t)

	selected := runHarness(t, model, "autonomous")
	kind, text := newestBlock(t, selected)
	if kind != blockNotice {
		t.Fatalf("a valid harness should be reported as a notice, got kind %d", kind)
	}
	if !strings.Contains(text, "autonomous") {
		t.Errorf("the notice should name the new harness, got %q", text)
	}
	if got := selected.app.HarnessProfile().ID; got != "autonomous" {
		t.Errorf("the app profile = %q, want autonomous", got)
	}

	refused := runHarness(t, selected, "nonsense")
	kind, text = newestBlock(t, refused)
	if kind != blockError {
		t.Fatalf("an unknown harness should be an error, got kind %d", kind)
	}
	if !strings.Contains(text, "critical") {
		t.Errorf("the error should name the alternatives, got %q", text)
	}
}
