package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// Each step reports the provider's own request size, so the status line can
// show it instead of a character estimate.
func TestRunEmitsTheMeasuredContextPerStep(t *testing.T) {
	step := []provider.StreamEvent{
		{Type: provider.EventTextDelta, Text: "done"},
		{Type: provider.EventUsage, Usage: &provider.Usage{PromptTokens: 9000, TotalTokens: 9000}},
		{Type: provider.EventUsage, Usage: &provider.Usage{PromptTokens: 2400, CompletionTokens: 40, TotalTokens: 2440}},
	}
	client := &fakeClient{steps: [][]provider.StreamEvent{step}}
	runner, env, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	if err := runner.Run(context.Background(), NewSession(env.Workspace, "test-model"), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	var measured []provider.Usage
	for _, event := range recorder.events {
		if event.Kind == EventContext {
			measured = append(measured, event.Usage)
		}
	}
	if len(measured) != 1 || measured[0].PromptTokens != 11400 || measured[0].CompletionTokens != 40 {
		t.Fatalf("measured = %+v, want one step summing its usage increments", measured)
	}
}

func TestMeasuredAndEstimatedHintsReadApart(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleUser, Content: "hello"}}
	measured := MeasuredContextHint(45200, messages, 200000)
	if !strings.HasPrefix(measured, "ctx: 45.2k/200.0k (22% of window)") {
		t.Fatalf("measured = %q", measured)
	}
	if estimated := ContextHint(messages, 100, 200000, ""); !strings.HasPrefix(estimated, "ctx: ~") {
		t.Fatalf("an estimate should be marked: %q", estimated)
	}
}
