package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// steerQueue returns the same shape the app's TakeSteer does: every message
// once, then nothing.
func steerQueue(messages ...string) func() []string {
	var mu sync.Mutex
	pending := messages
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := pending
		pending = nil
		return out
	}
}

// stopReason reads the reason the turn ended.
func stopReason(recorder *recorder) string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	reason := ""
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd {
			reason = event.StopReason
		}
	}
	return reason
}

// notices joins every notice the loop emitted, which is what the operator sees.
func notices(recorder *recorder) string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	var lines []string
	for _, event := range recorder.events {
		if event.Kind == EventNotice {
			lines = append(lines, event.Text)
		}
	}
	return strings.Join(lines, "\n")
}

// TestRunFoldsASteerIntoTheConversation is the point of steering: a message the
// operator types while the turn is running reaches the model at the next step
// instead of waiting for the turn to end.
func TestRunFoldsASteerIntoTheConversation(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "think", `{"thoughts":"first angle"}`)},
		{textChunk("done, and the tests are in the summary.")},
	}}
	runner, env, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.Steer = steerQueue("also check the tests")
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "change the parser"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !conversationContains(session, SteerPrefix) {
		t.Errorf("the steer never reached the model's conversation")
	}
	if !conversationContains(session, "also check the tests") {
		t.Errorf("the steer text is missing from the conversation")
	}
	if !strings.Contains(notices(recorder), "Steering: also check the tests") {
		t.Errorf("the operator should see the steer land, got:\n%s", notices(recorder))
	}
}

// TestSteeringResetsTheLoopGuard covers the recovery case: a model that repeats
// itself is about to have its turn closed, and an operator instruction is
// exactly the new information that should give it another chance.
func TestSteeringResetsTheLoopGuard(t *testing.T) {
	step := []provider.StreamEvent{callChunk("c", "think", `{"thoughts":"same angle"}`)}
	client := &fakeClient{steps: [][]provider.StreamEvent{step, step, step, step, step}}
	runner, env, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.MaxSteps = 5
	runner.Steer = func() []string { return []string{"try a different approach"} }
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "loop"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Without the steer the third identical call would have closed the turn.
	if got := stopReason(recorder); got != "step-cap" {
		t.Errorf("stop reason = %q, want step-cap: a steer must clear the repetition guard", got)
	}
}

// TestLoopGuardClosesEveryToolCallInABatch is the correctness rule behind the
// guard: a provider rejects a request whose assistant message carries tool
// calls with no matching results, so a guard that trips mid-batch has to answer
// the calls it cut short.
func TestLoopGuardClosesEveryToolCallInABatch(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{
			callChunk("c1", "think", `{"thoughts":"same"}`),
			callChunk("c2", "think", `{"thoughts":"same"}`),
			callChunk("c3", "think", `{"thoughts":"same"}`),
			callChunk("c4", "think", `{"thoughts":"same"}`),
		},
	}}
	runner, env, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "repeat yourself"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := stopReason(recorder); got != "loop-guard" {
		t.Fatalf("stop reason = %q, want loop-guard", got)
	}

	answered := map[string]bool{}
	for _, message := range session.Messages() {
		if message.Role == provider.RoleTool && message.ToolID != "" {
			answered[message.ToolID] = true
		}
	}
	for _, message := range session.Messages() {
		for _, call := range message.ToolCalls {
			if !answered[call.ID] {
				t.Errorf("tool call %s (%s) was cut short with no result, which a provider rejects", call.ID, call.Name)
			}
		}
	}
}

// TestCompletedTaskAsksForVerification is verify-on-stop applied per task: the
// step that finishes a plan item is where the check belongs, because the next
// item would otherwise build on a change nobody verified.
func TestCompletedTaskAsksForVerification(t *testing.T) {
	plan := func(status string) string {
		return `{"todos":[{"id":"t1","title":"Change the parser","status":"` + status + `"}]}`
	}
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{
			callChunk("c1", "todo_write", plan("in_progress")),
			callChunk("c2", "write_file", `{"path":"note.go","content":"package main"}`),
		},
		{callChunk("c3", "todo_write", plan("completed"))},
		{textChunk("The parser is changed.")},
	}}
	approve := func(ApprovalRequest) Decision { return DecisionAllowOnce }
	runner, env, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, approve)
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "change the parser"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The file was written and never checked, so the gate must have spoken.
	if !strings.Contains(notices(recorder), "Verifying before the next one") {
		t.Errorf("finishing a task with unverified edits must ask for verification, got:\n%s", notices(recorder))
	}
	if !conversationContains(session, VerifyNudgePrefix) {
		t.Errorf("the verification request never reached the model")
	}
}

// TestCompletedTaskWithoutEditsStaysQuiet is the other side of the gate: a task
// with nothing to verify must not generate noise.
func TestCompletedTaskWithoutEditsStaysQuiet(t *testing.T) {
	plan := `{"todos":[{"id":"t1","title":"Read the parser","status":"completed"}]}`
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "todo_write", `{"todos":[{"id":"t1","title":"Read the parser","status":"in_progress"}]}`)},
		{callChunk("c2", "todo_write", plan)},
		{textChunk("I read it.")},
	}}
	runner, env, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "read the parser"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(notices(recorder), "Verifying before the next one") {
		t.Errorf("a task that changed no code must not ask for verification, got:\n%s", notices(recorder))
	}
}

// TestTaskJustCompleted pins the detection rule on its own, including the case
// that matters most: the plan being rewritten wholesale must not read as a
// completion.
func TestTaskJustCompleted(t *testing.T) {
	inProgress := []Todo{{ID: "t1", Title: "one", Status: "in_progress"}, {ID: "t2", Title: "two", Status: "pending"}}
	cases := []struct {
		name   string
		before []Todo
		after  []Todo
		want   bool
	}{
		{"the active item is completed", inProgress, []Todo{{ID: "t1", Status: "completed"}, {ID: "t2", Status: "pending"}}, true},
		{"a pending item is completed", inProgress, []Todo{{ID: "t1", Status: "in_progress"}, {ID: "t2", Status: "completed"}}, false},
		{"nothing moved", inProgress, inProgress, false},
		{"the plan was replaced", inProgress, []Todo{{ID: "fresh", Status: "completed"}}, false},
		{"there was no active item", nil, []Todo{{ID: "t1", Status: "completed"}}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := taskJustCompleted(testCase.before, testCase.after); got != testCase.want {
				t.Errorf("taskJustCompleted = %v, want %v", got, testCase.want)
			}
		})
	}
}

// conversationContains reports whether any message carries the text.
func conversationContains(session *Session, text string) bool {
	for _, message := range session.Messages() {
		if strings.Contains(message.Content, text) {
			return true
		}
	}
	return false
}
