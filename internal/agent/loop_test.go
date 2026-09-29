package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

// fakeClient replays a fixed list of event batches, one per Stream call.
type fakeClient struct {
	mu    sync.Mutex
	steps [][]provider.StreamEvent
	calls int
}

func (f *fakeClient) ID() string { return "fake" }

func (f *fakeClient) Stream(_ context.Context, _ provider.ChatRequest, emit func(provider.StreamEvent) error) error {
	f.mu.Lock()
	index := f.calls
	f.calls++
	f.mu.Unlock()
	if index >= len(f.steps) {
		return nil
	}
	for _, event := range f.steps[index] {
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}

// recorder collects events for assertions.
type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) emit(event Event) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *recorder) kinds() []EventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	kinds := make([]EventKind, 0, len(r.events))
	for _, event := range r.events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func (r *recorder) has(kind EventKind) bool {
	for _, event := range r.kinds() {
		if event == kind {
			return true
		}
	}
	return false
}

func (r *recorder) toolLabels() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var labels []string
	for _, event := range r.events {
		if event.Kind == EventToolEnd {
			labels = append(labels, event.ToolLabel)
		}
	}
	return labels
}

func textChunk(text string) provider.StreamEvent {
	return provider.StreamEvent{Type: provider.EventTextDelta, Text: text}
}

func callChunk(id, name, arguments string) provider.StreamEvent {
	return provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{ID: id, Name: name, Arguments: arguments}}
}

func newTestRunner(t *testing.T, client provider.Client, policy *ApprovalPolicy, approve func(ApprovalRequest) Decision) (*Runner, *Env, *recorder) {
	t.Helper()
	workspace := t.TempDir()
	recorder := &recorder{}
	env := &Env{
		Workspace: workspace,
		Config:    config.Default(),
		Todos:     NewTodoStore(),
		Memory:    NewMemory(workspace),
		Trusted:   true,
		Emit:      recorder.emit,
		Approve:   approve,
	}
	runner := &Runner{
		Client:   client,
		Model:    "test-model",
		Config:   config.Default(),
		Env:      env,
		Tools:    DefaultRegistry(),
		Policy:   policy,
		MaxSteps: 5,
	}
	return runner, env, recorder
}

func TestRunExecutesToolThenFinishes(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{
			{Type: provider.EventReasoningDelta, Text: "I should read the file."},
			callChunk("c1", "read_file", `{"path":"note.txt"}`),
		},
		{textChunk("The file says "), textChunk("hello.")},
	}}
	runner, env, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	if err := os.WriteFile(filepath.Join(env.Workspace, "note.txt"), []byte("hello from disk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "what does note.txt say?"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	kinds := recorder.kinds()
	for _, want := range []EventKind{EventTurnStart, EventThinking, EventReasoned, EventToolStart, EventToolEnd, EventText, EventTurnEnd} {
		found := false
		for _, kind := range kinds {
			if kind == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s event; got %v", want, kinds)
		}
	}

	if labels := recorder.toolLabels(); len(labels) != 1 || !strings.Contains(labels[0], "Read") {
		t.Errorf("the tool should report past tense, got %v", labels)
	}

	// The tool result must be in the conversation, correlated by call id.
	var found bool
	for _, message := range session.Messages() {
		if message.Role == provider.RoleTool {
			found = true
			if message.ToolID != "c1" {
				t.Errorf("tool result id = %q, want c1", message.ToolID)
			}
			if !strings.Contains(message.Content, "hello from disk") {
				t.Errorf("the tool output was not fed back: %q", message.Content)
			}
		}
	}
	if !found {
		t.Fatalf("the tool result is missing from the session")
	}

	// The final assistant text must be persisted.
	messages := session.Messages()
	last := messages[len(messages)-1]
	if last.Role != provider.RoleAssistant || !strings.Contains(last.Content, "hello.") {
		t.Errorf("the final answer is missing: %+v", last)
	}
}

func TestRunDeniedToolIsReportedAndNotExecuted(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "write_file", `{"path":"should-not-exist.txt","content":"nope"}`)},
		{textChunk("Understood, I will not write it.")},
	}}
	approve := func(ApprovalRequest) Decision { return DecisionDeny }
	runner, env, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAsk}, approve)
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "write a file"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(env.Workspace, "should-not-exist.txt")); err == nil {
		t.Fatalf("a denied tool must not run")
	}
	var denial string
	for _, message := range session.Messages() {
		if message.Role == provider.RoleTool {
			denial = message.Content
		}
	}
	if !strings.Contains(strings.ToLower(denial), "denied") {
		t.Errorf("the model must be told the call was denied, got %q", denial)
	}
	if !recorder.has(EventToolEnd) {
		t.Errorf("a denied call still reports completion")
	}
}

func TestRunApprovalSessionAllowanceSkipsSecondPrompt(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "write_file", `{"path":"a.txt","content":"one"}`)},
		{callChunk("c2", "write_file", `{"path":"b.txt","content":"two"}`)},
		{textChunk("Both files written.")},
	}}
	prompts := 0
	approve := func(ApprovalRequest) Decision {
		prompts++
		return DecisionAllowSession
	}
	runner, env, _ := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAsk}, approve)
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "write two files"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if prompts != 1 {
		t.Errorf("a session allowance should skip the second prompt, got %d prompts", prompts)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(env.Workspace, name)); err != nil {
			t.Errorf("%s should have been written: %v", name, err)
		}
	}
}

func TestRunContinuesPastTheStepBudgetWhileProgressing(t *testing.T) {
	// An interactive turn is segmented: when a segment of MaxSteps still ends by
	// asking for tools, the next segment runs. A small configured MaxSteps used to
	// pause a working task mid-way, which the operator saw as the agent giving up
	// while the context window was barely used.
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "list_directory", `{"path":"."}`)},
		{callChunk("c2", "list_directory", `{"path":"./"}`)},
		{callChunk("c3", "list_directory", `{"path":"./."}`)},
		{callChunk("c4", "list_directory", `{"path":"././"}`)},
		{callChunk("c5", "list_directory", `{"path":"././."}`)},
		{textChunk("done")},
	}}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.MaxSteps = 3
	runner.TurnSegments = 3
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "keep working"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls <= 3 {
		t.Errorf("provider calls = %d, want the turn to continue past the 3-step segment", calls)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd && event.StopReason != "stop" {
			t.Errorf("stop reason = %q, want stop after the model answered", event.StopReason)
		}
	}
}

func TestRunStillPausesAtTheStepCeiling(t *testing.T) {
	// The segment count is the absolute ceiling: a run that is always ready to
	// dispatch another call still ends, so nothing loops forever.
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "list_directory", `{"path":"."}`)},
		{callChunk("c2", "list_directory", `{"path":"./"}`)},
		{callChunk("c3", "list_directory", `{"path":"./."}`)},
		{callChunk("c4", "list_directory", `{"path":"././"}`)},
		{callChunk("c5", "list_directory", `{"path":"././."}`)},
		{callChunk("c6", "list_directory", `{"path":"./././"}`)},
	}}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.MaxSteps = 2
	runner.TurnSegments = 2
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "never stop"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls > 4 {
		t.Errorf("provider calls = %d, want the ceiling of 2 segments of 2 steps", calls)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd && event.StopReason != "step-cap" {
			t.Errorf("stop reason = %q, want step-cap at the ceiling", event.StopReason)
		}
	}
}

func TestRunStopsAtStepBudget(t *testing.T) {
	// Every step asks for another tool call, and each call differs so the loop
	// guard is not what ends the run: the step cap has to be.
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "list_directory", `{"path":"."}`)},
		{callChunk("c2", "list_directory", `{"path":"./"}`)},
		{callChunk("c3", "list_directory", `{"path":"./."}`)},
	}}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.MaxSteps = 3
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "loop forever"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !recorder.has(EventTurnEnd) {
		t.Fatalf("the turn must end with an EventTurnEnd")
	}
	var stopReason string
	recorder.mu.Lock()
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd {
			stopReason = event.StopReason
		}
	}
	recorder.mu.Unlock()
	if stopReason != "step-cap" {
		t.Errorf("stop reason = %q, want step-cap", stopReason)
	}
	// The pause has to tell the operator how to resume, or the turn looks like
	// it simply gave up.
	var notice string
	recorder.mu.Lock()
	for _, event := range recorder.events {
		if event.Kind == EventNotice && strings.Contains(event.Text, "step") {
			notice = event.Text
		}
	}
	recorder.mu.Unlock()
	for _, want := range []string{"continue", "Paused"} {
		if !strings.Contains(notice, want) {
			t.Errorf("the step-cap notice should mention %q, got %q", want, notice)
		}
	}
}

func TestRunPropagatesStreamErrors(t *testing.T) {
	client := &errorClient{}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "hello"); err != nil {
		t.Fatalf("Run should report the failure as an event, not an error: %v", err)
	}
	if !recorder.has(EventError) {
		t.Fatalf("the stream failure must be surfaced as an EventError")
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd && event.StopReason != "error" {
			t.Errorf("stop reason = %q, want error", event.StopReason)
		}
	}
}

// errorClient always fails, which is how a network error reaches the loop.
type errorClient struct{}

func (errorClient) ID() string { return "error" }

func (errorClient) Stream(context.Context, provider.ChatRequest, func(provider.StreamEvent) error) error {
	return context.DeadlineExceeded
}

func TestRunSubagentUsesReadOnlyTools(t *testing.T) {
	registry := readOnlyTools()
	for _, forbidden := range []string{"write_file", "edit", "run_command", "delete_file"} {
		if _, ok := registry.Lookup(forbidden); ok {
			t.Errorf("a review subagent must not be able to call %s", forbidden)
		}
	}
	for _, allowed := range []string{"read_file", "grep", "glob", "list_directory"} {
		if _, ok := registry.Lookup(allowed); !ok {
			t.Errorf("a review subagent should be able to call %s", allowed)
		}
	}
}

func TestWorkerSubagentKeepsFullToolset(t *testing.T) {
	registry := subagentRegistry(string(SubagentGeneral), 0)
	for _, allowed := range []string{"read_file", "write_file", "edit", "run_command", "run_checks", "run_subagent"} {
		if _, ok := registry.Lookup(allowed); !ok {
			t.Errorf("a worker subagent should be able to call %s", allowed)
		}
	}
	capped := subagentRegistry(string(SubagentGeneral), MaxSubagentDepth)
	if _, ok := capped.Lookup("run_subagent"); ok {
		t.Errorf("the spawn tool must be withheld at the depth cap")
	}
}

func TestLookupSubagentFallsBackToGeneral(t *testing.T) {
	if got := LookupSubagent("nope"); got.Type != SubagentGeneral {
		t.Errorf("unknown type = %q, want general", got.Type)
	}
	if !SubagentIsReadOnly(string(SubagentCodeReview)) {
		t.Errorf("code-review should be read-only by design")
	}
	if SubagentIsReadOnly(string(SubagentBuilder)) {
		t.Errorf("builder should keep the full toolset")
	}
}
