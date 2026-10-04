package agent

import (
	"context"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

// TestRunSubagentReturnsSpend pins the accounting a delegated run hands back:
// the throwaway child session's tokens and dollars have to come back up, or
// the parent turn, /cost and the cost budget all under-count the work.
func TestRunSubagentReturnsSpend(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{usageChunk(300, 100), textChunk("the answer")},
	}}
	parent := testEnv(t)
	parent.Config = config.Default()

	_, spend, err := RunSubagent(context.Background(), parent, client, provider.Model{ID: "test-model"}, string(SubagentGeneral), "compute", 4)
	if err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if spend.Usage.PromptTokens != 300 || spend.Usage.CompletionTokens != 100 {
		t.Errorf("spend.Usage = %+v, want 300 in and 100 out", spend.Usage)
	}
}

// TestParentTurnFoldsDelegatedSpend drives a main turn that calls run_subagent
// and asserts the delegated tokens and dollars land in the parent's turn total
// and session cost rather than dying with the child's throwaway session.
func TestParentTurnFoldsDelegatedSpend(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{usageChunk(50, 10), callChunk("c1", "run_subagent", `{"prompt":"summarise"}`)},
		{textChunk("done.")},
	}}
	runner, env, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	env.RunSubagent = func(ctx context.Context, subType, prompt string) (string, SubagentSpend, error) {
		return "the report", SubagentSpend{
			Usage:     provider.Usage{PromptTokens: 500, CompletionTokens: 40, TotalTokens: 540},
			Cost:      0.0123,
			CostKnown: true,
		}, nil
	}
	// Give the model a real price so the parent's own usage also costs money.
	runner.Pricing = provider.Pricing{InputPerMillion: 2, OutputPerMillion: 6}
	runner.CostKnown = true

	session := NewSession(env.Workspace, "test-model")
	if err := runner.Run(context.Background(), session, "delegate this"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var end Event
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd {
			end = event
		}
	}
	// 50 prompt + 10 completion from the parent step, plus 500 prompt + 40
	// completion from the delegated run.
	if end.Usage.PromptTokens != 550 || end.Usage.CompletionTokens != 50 {
		t.Errorf("turn usage = %+v, want 550 prompt and 50 completion including the subagent", end.Usage)
	}
	// The delegated $0.0123 must survive into the running session cost.
	if end.CostUSD < 0.0123 {
		t.Errorf("turn cost = %f, want at least the delegated 0.0123", end.CostUSD)
	}
	if session.Cost() < 0.0123 {
		t.Errorf("session cost = %f, want at least the delegated 0.0123", session.Cost())
	}
}
