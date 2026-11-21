package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// The plan-mode handshake end to end: the turn starts in plan mode, the model
// submits a plan, the operator approves with edits, and the same turn then
// writes a file that plan mode would have blocked.
func TestApprovedPlanLetsTheSameTurnMakeChanges(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "submit_plan", `{"plan":"1. write notes.txt"}`)},
		{callChunk("c2", "write_file", `{"path":"notes.txt","content":"done"}`)},
		{{Type: provider.EventTextDelta, Text: "Wrote the file."}},
	}}
	policy := &ApprovalPolicy{Mode: ApprovalPlan}
	runner, env, _ := newTestRunner(t, client, policy, nil)
	env.Config.ApprovalMode = "plan"
	var shown string
	env.ReviewPlan = func(plan string) (PlanReview, error) {
		shown = plan
		return PlanReview{Approved: true, Mode: ApprovalEdits}, nil
	}

	if err := runner.Run(context.Background(), NewSession(env.Workspace, "test-model"), "add notes"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if shown != "1. write notes.txt" {
		t.Fatalf("the operator should see the plan, got %q", shown)
	}
	if policy.Mode != ApprovalEdits || env.Config.ApprovalMode != "edits" {
		t.Fatalf("the turn should leave plan mode: policy %s, config %s", policy.Mode, env.Config.ApprovalMode)
	}
	data, err := os.ReadFile(filepath.Join(env.Workspace, "notes.txt"))
	if err != nil || string(data) != "done" {
		t.Fatalf("the approved turn should have written the file: %v %q", err, data)
	}
}

func TestRejectedPlanStaysInPlanMode(t *testing.T) {
	env := &Env{Workspace: t.TempDir(), ReviewPlan: func(string) (PlanReview, error) {
		return PlanReview{Feedback: "use a config flag instead"}, nil
	}}
	result, _ := (&submitPlanTool{}).Run(context.Background(), env, map[string]any{"plan": "do it"})
	if result.IsError || result.ApprovalMode != "" || !strings.Contains(result.Output, "use a config flag instead") {
		t.Fatalf("result = %+v", result)
	}
}

func TestPlanReviewFallsBackToAQuestion(t *testing.T) {
	var asked string
	env := &Env{Workspace: t.TempDir(), Ask: func(question string, options []string) (string, error) {
		asked = question
		return options[0], nil
	}}
	result, _ := (&submitPlanTool{}).Run(context.Background(), env, map[string]any{"plan": "the plan"})
	if !strings.Contains(asked, "the plan") || result.ApprovalMode != ApprovalAsk {
		t.Fatalf("asked %q, result %+v", asked, result)
	}
	feedback, _ := AskPlanReview(func(string, []string) (string, error) { return "split it in two", nil }, "x")
	if feedback.Approved || feedback.Feedback != "split it in two" {
		t.Fatalf("free text is feedback: %+v", feedback)
	}
}
