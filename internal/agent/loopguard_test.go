package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// TestLoopGuardTripsOnRepeatedCalls is the no-loop contract: the same tool
// call over and over first earns recovery nudges, and a run that ignores
// them ends with an explanation instead of burning the whole step budget.
func TestLoopGuardTripsOnRepeatedCalls(t *testing.T) {
	// The repeated call has to fail: a rerun that succeeds is progress and
	// resets the repetition state, so a successful identical call never trips
	// the guard. A call that keeps failing unchanged is the genuine loop.
	step := []provider.StreamEvent{callChunk("c", "read_file", `{"path":"missing.txt"}`)}
	// Enough repeats to exhaust the nudges and then loop for real: each nudge
	// resets the repeat counter, so the trip needs MaxLoopNudges rounds of
	// three identical calls.
	repeats := 1 + MaxLoopNudges
	steps := make([][]provider.StreamEvent, 0, repeats*3+1)
	for i := 0; i < repeats*3; i++ {
		steps = append(steps, step)
	}
	client := &fakeClient{steps: steps}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.MaxSteps = 40
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "loop forever"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var stopReason string
	var notice string
	var nudged bool
	recorder.mu.Lock()
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd {
			stopReason = event.StopReason
		}
		if event.Kind == EventNotice && strings.Contains(event.Text, "repeated") {
			notice = event.Text
		}
		if event.Kind == EventNotice && strings.Contains(event.Text, "recovery attempt") {
			nudged = true
		}
	}
	recorder.mu.Unlock()
	if stopReason != "loop-guard" {
		t.Errorf("stop reason = %q, want loop-guard", stopReason)
	}
	if notice == "" {
		t.Errorf("the guard should explain the stop")
	}
	if !nudged {
		t.Errorf("the guard should try to recover before stopping")
	}
}

// TestLoopGuardNudgeRecovers is the complaint that drove the change: a model
// that repeats one call is not necessarily stuck, it may be one instruction
// away from choosing a different approach. The nudge arrives as a user
// message, the model answers it, and the run finishes cleanly.
func TestLoopGuardNudgeRecovers(t *testing.T) {
	repeat := []provider.StreamEvent{callChunk("c", "list_directory", `{"path":"."}`)}
	steps := [][]provider.StreamEvent{
		repeat, repeat, repeat,
		{callChunk("c2", "list_directory", `{"path":"sub"}`)},
		{textChunk("recovered.")},
	}
	client := &fakeClient{steps: steps}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.MaxSteps = 10
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "try once"); err != nil {
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
		t.Errorf("stop reason = %q, want a clean stop after the nudge", stopReason)
	}
}

// TestErrorStreakNudgesInsteadOfStopping covers a stuck shape: every call is
// new, but all of them fail. Four in a row must not end the run; it asks the
// model to diagnose the first error and lets it try again, which is how a run
// recovers instead of dying on a rough patch.
func TestErrorStreakNudgesInsteadOfStopping(t *testing.T) {
	steps := [][]provider.StreamEvent{
		{callChunk("c1", "no_such_tool_a", `{}`)},
		{callChunk("c2", "no_such_tool_b", `{}`)},
		{callChunk("c3", "no_such_tool_c", `{}`)},
		{callChunk("c4", "no_such_tool_d", `{}`)},
		{textChunk("recovered after diagnosing.")},
	}
	client := &fakeClient{steps: steps}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.MaxSteps = 10
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "fail forward"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var stopReason string
	var nudged bool
	recorder.mu.Lock()
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd {
			stopReason = event.StopReason
		}
		if event.Kind == EventNotice && strings.Contains(event.Text, "Diagnose") {
			nudged = true
		}
	}
	recorder.mu.Unlock()
	if stopReason != "stop" {
		t.Errorf("stop reason = %q, want a clean stop after the nudge", stopReason)
	}
	if !nudged {
		t.Errorf("the error streak should nudge the model to diagnose")
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
