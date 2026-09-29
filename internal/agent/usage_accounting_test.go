package agent

import (
	"context"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// TestTurnUsageReachesTheSessionOnce pins the accounting the app wires up.
//
// In production every usage event the loop emits is folded into the session by
// the emitter, because App.emit calls AddUsage, which updates the app total and
// the session together. The loop then folded the turn total into the same
// session a second time before emitting EventTurnEnd, so a one step turn was
// recorded with twice its tokens and the session file disagreed with /cost.
func TestTurnUsageReachesTheSessionOnce(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{usageChunk(1000, 200), textChunk("done.")},
	}}
	runner, env, _ := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	session := NewSession(env.Workspace, "test-model")
	// The production sink, reduced to the part that matters here: a usage event
	// is folded into the same session the runner records into.
	env.Emit = func(event Event) {
		if event.Kind == EventUsage {
			session.AddUsage(event.Usage)
		}
	}

	if err := runner.Run(context.Background(), session, "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	usage := session.Usage()
	if usage.PromptTokens != 1000 || usage.CompletionTokens != 200 {
		t.Errorf("session usage = %+v, want 1000 in and 200 out counted once", usage)
	}
}
