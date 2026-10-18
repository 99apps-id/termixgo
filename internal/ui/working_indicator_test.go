package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
)

// TestWorkingIndicatorIsShownOnce pins the duplication the operator sees as
// "working (0s)" printed both above and below the composer. The status row and
// the hints row both carried it while a turn ran, so the elapsed time appeared
// twice on one screen.
func TestWorkingIndicatorIsShownOnce(t *testing.T) {
	model := chatModel(t)
	resize(model, 100, 30)
	model.running = true
	model.runStarted = time.Now()

	view := display(model)
	if got := strings.Count(view, "working"); got != 1 {
		t.Errorf("the working indicator appears %d times, want exactly 1:\n%s", got, view)
	}
	// The hints row is the one that is on screen, so that is where it must be.
	if !strings.Contains(stripANSI(model.viewHints()), "working") {
		t.Errorf("the hints row should carry the indicator:\n%s", stripANSI(model.viewHints()))
	}
	if strings.Contains(stripANSI(model.viewStatus()), "working") {
		t.Errorf("the status row should not repeat it while the hints row is shown:\n%s", stripANSI(model.viewStatus()))
	}
}

// TestWorkingIndicatorSurvivesAHiddenHintsRow is the other half of the rule: a
// command menu replaces the hints row, so the indicator has to move to the
// status line rather than disappear, and it must still appear only once.
func TestWorkingIndicatorSurvivesAHiddenHintsRow(t *testing.T) {
	model := chatModel(t)
	resize(model, 100, 30)
	model.running = true
	model.runStarted = time.Now()

	model = openSlashPalette(t, model)
	for _, stroke := range []string{"s", "t"} {
		model = press(t, model, stroke)
	}
	if len(model.slashMatches) == 0 {
		t.Fatalf("/st matched no command, so no menu replaces the hints row")
	}
	if model.hintsVisible() {
		t.Fatalf("a slash menu should hide the hints row")
	}
	view := display(model)
	if got := strings.Count(view, "working"); got != 1 {
		t.Errorf("with the menu open the indicator appears %d times, want exactly 1:\n%s", got, view)
	}
	if !strings.Contains(stripANSI(model.viewStatus()), "working") {
		t.Errorf("the status row should carry the indicator once the hints row is gone:\n%s", stripANSI(model.viewStatus()))
	}
}

// TestWorkingIndicatorSurvivesAnApprovalPrompt covers the other surface that
// replaces the hints row: while the operator decides, the turn is still running
// and the frame should say so exactly once.
func TestWorkingIndicatorSurvivesAnApprovalPrompt(t *testing.T) {
	model := chatModel(t)
	resize(model, 100, 30)
	model.running = true
	model.runStarted = time.Now()
	model.pendingApproval = &agent.ApprovalRequest{Tool: "edit", Risk: "edit", Detail: "Editing main.go"}

	view := display(model)
	if got := strings.Count(view, "working"); got != 1 {
		t.Errorf("with an approval prompt the indicator appears %d times, want exactly 1:\n%s", got, view)
	}
}
