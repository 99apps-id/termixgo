package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// TestLoopGuardTripsOnRepeatedCalls is the no-loop contract: the same tool
// call three times in a row ends the run with an explanation, instead of
// burning the whole step budget on one stuck idea.
func TestLoopGuardTripsOnRepeatedCalls(t *testing.T) {
	step := []provider.StreamEvent{callChunk("c", "list_directory", `{"path":"."}`)}
	client := &fakeClient{steps: [][]provider.StreamEvent{step, step, step, step, step}}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.MaxSteps = 10
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "loop forever"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var stopReason string
	var notice string
	recorder.mu.Lock()
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd {
			stopReason = event.StopReason
		}
		if event.Kind == EventNotice && strings.Contains(event.Text, "repeated") {
			notice = event.Text
		}
	}
	recorder.mu.Unlock()
	if stopReason != "loop-guard" {
		t.Errorf("stop reason = %q, want loop-guard", stopReason)
	}
	if notice == "" {
		t.Errorf("the guard should explain the stop")
	}
}

// TestLoopGuardTripsOnErrorStreak covers a different stuck shape: every call
// is new, but all of them fail. Four in a row ends the run so the operator
// can diagnose the first error instead of watching retries.
func TestLoopGuardTripsOnErrorStreak(t *testing.T) {
	steps := [][]provider.StreamEvent{
		{callChunk("c1", "no_such_tool_a", `{}`)},
		{callChunk("c2", "no_such_tool_b", `{}`)},
		{callChunk("c3", "no_such_tool_c", `{}`)},
		{callChunk("c4", "no_such_tool_d", `{}`)},
	}
	client := &fakeClient{steps: steps}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.MaxSteps = 10
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "fail forward"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var stopReason string
	recorder.mu.Lock()
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd {
			stopReason = event.StopReason
		}
	}
	recorder.mu.Unlock()
	if stopReason != "loop-guard" {
		t.Errorf("stop reason = %q, want loop-guard", stopReason)
	}
}

// TestLoopGuardTripsOnEmptySteps covers the idle model: steps with no text
// and no calls must not spin until the budget runs out.
func TestLoopGuardTripsOnEmptySteps(t *testing.T) {
	client := &fakeClient{steps: nil}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.MaxSteps = 10
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "say nothing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var stopReason string
	recorder.mu.Lock()
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd {
			stopReason = event.StopReason
		}
	}
	recorder.mu.Unlock()
	if stopReason != "loop-guard" {
		t.Errorf("stop reason = %q, want loop-guard", stopReason)
	}
	// An idle step must not be filed as an assistant turn. An empty assistant
	// message, or two in a row, is what a weaker OpenAI-compatible model reads
	// as a malformed turn and answers an earlier message from.
	for _, message := range session.Messages() {
		if message.Role == provider.RoleAssistant && strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0 {
			t.Errorf("an empty model step was stored as an assistant turn: %+v", message)
		}
	}
}

// TestLoopGuardLetsDistinctWorkThrough proves the guard is a tripwire, not
// a ceiling: varied successful calls keep running until the model stops.
func TestLoopGuardLetsDistinctWorkThrough(t *testing.T) {
	steps := [][]provider.StreamEvent{
		{callChunk("c1", "think", `{"thoughts":"first angle"}`)},
		{callChunk("c2", "think", `{"thoughts":"second angle"}`)},
		{textChunk("done.")},
	}
	client := &fakeClient{steps: steps}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "think twice"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var stopReason string
	recorder.mu.Lock()
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd {
			stopReason = event.StopReason
		}
	}
	recorder.mu.Unlock()
	if stopReason != "stop" {
		t.Errorf("stop reason = %q, want a clean stop", stopReason)
	}
}
