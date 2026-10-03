package agent

import (
	"context"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// TestEveryStopReasonReportsUsage keeps the audit ledger honest.
//
// The clean stop emitted the turn's summed usage, and the early stops did not,
// so App.recordAudit - which reads usage off exactly this event - wrote a turn
// cut short by the step ceiling, a loop guard or a stop as one that spent
// nothing. The ledger is the only record of what an interrupted turn cost.
func TestEveryStopReasonReportsUsage(t *testing.T) {
	usageEvent := func(prompt, completion int) provider.StreamEvent {
		return provider.StreamEvent{Type: provider.EventUsage, Usage: &provider.Usage{
			PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion,
		}}
	}
	tests := []struct {
		name       string
		maxSteps   int
		steps      [][]provider.StreamEvent
		stopReason string
	}{
		{
			name:     "step ceiling",
			maxSteps: 2,
			// Every step dispatches a call, so the loop is cut off while it
			// is still working rather than finishing.
			steps: [][]provider.StreamEvent{
				{usageEvent(100, 10), callChunk("c1", "list_directory", `{"path":"."}`)},
				{usageEvent(200, 20), callChunk("c2", "list_directory", `{"path":"./a"}`)},
				{usageEvent(300, 30), callChunk("c3", "list_directory", `{"path":"./b"}`)},
			},
			stopReason: "step-cap",
		},
		{
			name:     "loop guard",
			maxSteps: 10,
			// The same call three times in a row is what trips the guard.
			steps: [][]provider.StreamEvent{
				{usageEvent(100, 10), callChunk("c1", "list_directory", `{"path":"."}`)},
				{usageEvent(100, 10), callChunk("c2", "list_directory", `{"path":"."}`)},
				{usageEvent(100, 10), callChunk("c3", "list_directory", `{"path":"."}`)},
				{usageEvent(100, 10), callChunk("c4", "list_directory", `{"path":"."}`)},
			},
			stopReason: "loop-guard",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeClient{steps: test.steps}
			runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
			runner.MaxSteps = test.maxSteps
			session := NewSession(t.TempDir(), "test-model")

			if err := runner.Run(context.Background(), session, "keep going"); err != nil {
				t.Fatalf("Run: %v", err)
			}

			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			var end Event
			found := false
			for _, event := range recorder.events {
				if event.Kind == EventTurnEnd {
					end = event
					found = true
				}
			}
			if !found {
				t.Fatalf("no EventTurnEnd was emitted")
			}
			if end.StopReason != test.stopReason {
				t.Fatalf("stop reason = %q, want %q for this fixture", end.StopReason, test.stopReason)
			}
			if end.Usage.PromptTokens == 0 || end.Usage.CompletionTokens == 0 {
				t.Errorf("EventTurnEnd on the %q path carried usage %+v, want the turn total",
					end.StopReason, end.Usage)
			}
		})
	}
}
