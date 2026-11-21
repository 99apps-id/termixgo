package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
)

func openPlan(t *testing.T) (*Model, chan agent.PlanReview) {
	t.Helper()
	model := chatModel(t)
	reply := make(chan agent.PlanReview, 1)
	next, _ := model.Update(planReviewMsg{plan: "1. Add a flag\n2. Test it", reply: reply})
	return next.(*Model), reply
}

func TestPlanReviewShowsThePlanAndApproves(t *testing.T) {
	model, reply := openPlan(t)
	view := stripANSI(model.View())
	for _, want := range []string{"Plan for approval", "Add a flag", "Ready to code?", "1. Yes, and accept edits automatically"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	press(t, model, "1")
	if got := <-reply; !got.Approved || got.Mode != agent.ApprovalEdits {
		t.Fatalf("1 should approve with edits: %+v", got)
	}
	if model.pendingPlan != nil {
		t.Fatalf("the dialog should close")
	}
}

func TestPlanReviewSendsFeedback(t *testing.T) {
	model, reply := openPlan(t)
	model = press(t, model, "3")
	if !model.planFeedback {
		t.Fatalf("keep planning should open the feedback field")
	}
	for _, r := range "use a config flag" {
		model = press(t, model, string(r))
	}
	press(t, model, "enter")
	if got := <-reply; got.Approved || got.Feedback != "use a config flag" {
		t.Fatalf("feedback = %+v", got)
	}
}

func TestPlanReviewEscapeKeepsPlanning(t *testing.T) {
	model, reply := openPlan(t)
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = next.(*Model)
	model = pressEsc(t, model)
	if got := <-reply; got.Approved {
		t.Fatalf("Esc must not approve: %+v", got)
	}
	if model.pendingPlan != nil {
		t.Fatalf("the dialog should close")
	}
}

func TestPlanReviewIsReleasedWhenTheTurnEnds(t *testing.T) {
	model, reply := openPlan(t)
	model.running = true
	model.Update(runDoneMsg{})
	select {
	case got := <-reply:
		if got.Approved {
			t.Fatalf("an abandoned review must not approve")
		}
	default:
		t.Fatalf("the waiting agent must be released")
	}
}
