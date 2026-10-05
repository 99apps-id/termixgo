package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// taskClientStub answers a task call with fixed text and records what it was
// asked, so a test can assert on the prompt and the model id.
type taskClientStub struct {
	mu       sync.Mutex
	answer   string
	err      error
	requests []provider.ChatRequest
}

func (c *taskClientStub) ID() string { return "task-stub" }

func (c *taskClientStub) Stream(_ context.Context, req provider.ChatRequest, emit func(provider.StreamEvent) error) error {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	answer, err := c.answer, c.err
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return emit(provider.StreamEvent{Type: provider.EventTextDelta, Text: answer})
}

// TestTaskCallSendsOneShotRequest pins the shape of an internal call: one user
// message, the task system prompt, no tools, and the effort the caller chose.
func TestTaskCallSendsOneShotRequest(t *testing.T) {
	client := &taskClientStub{answer: "done"}
	answer, err := taskCall(context.Background(), client, "cheap-model", provider.EffortLow, "be brief", "do the thing")
	if err != nil {
		t.Fatalf("taskCall: %v", err)
	}
	if answer != "done" {
		t.Errorf("answer = %q, want the streamed text", answer)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.requests) != 1 {
		t.Fatalf("the task should be one request, got %d", len(client.requests))
	}
	request := client.requests[0]
	if request.Model != "cheap-model" {
		t.Errorf("model = %q, want the task model", request.Model)
	}
	if request.Effort != provider.EffortLow {
		t.Errorf("effort = %q, want low", request.Effort)
	}
	if len(request.Tools) != 0 {
		t.Errorf("an internal task must not be offered tools, got %d", len(request.Tools))
	}
	if len(request.Messages) != 1 || request.Messages[0].Role != provider.RoleUser {
		t.Errorf("messages = %+v, want one user message", request.Messages)
	}
	if request.MaxTokens != taskMaxTokens {
		t.Errorf("MaxTokens = %d, want the task cap %d", request.MaxTokens, taskMaxTokens)
	}
}

// TestTaskCallReportsAProviderFailure keeps a failed call visible to the caller,
// which treats it as "fall back to the plain trim" rather than as an answer.
func TestTaskCallReportsAProviderFailure(t *testing.T) {
	client := &taskClientStub{err: errors.New("provider exploded")}
	if _, err := taskCall(context.Background(), client, "m", "", "s", "p"); err == nil {
		t.Fatalf("a failed task call must return an error")
	}
}

// TestTaskCallWithoutAClientFails keeps a missing client from reading as an
// empty answer, which would store a blank brief.
func TestTaskCallWithoutAClientFails(t *testing.T) {
	if _, err := taskCall(context.Background(), nil, "m", "", "s", "p"); err == nil {
		t.Fatalf("a nil client must return an error")
	}
}

// TestCleanTitleTrimsModelNoise covers what a model actually returns: quotes, a
// trailing newline, and sometimes a whole sentence.
func TestCleanTitleTrimsModelNoise(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"Fix the login redirect"`, "Fix the login redirect"},
		{"Fix the login redirect\nHere is why: it was broken", "Fix the login redirect"},
		{"  spaced  ", "spaced"},
		{"```code```", "code"},
		{"", ""},
		{"   ", ""},
	}
	for _, testCase := range cases {
		if got := cleanTitle(testCase.in); got != testCase.want {
			t.Errorf("cleanTitle(%q) = %q, want %q", testCase.in, got, testCase.want)
		}
	}
	if got := cleanTitle(strings.Repeat("a", 200)); len(got) != 60 {
		t.Errorf("a long title should be clipped to 60 bytes, got %d", len(got))
	}
}

// TestRenderTranscriptNamesTurnsWithoutToolPayloads keeps the brief cheap: the
// raw tool arguments are exactly what made the history too large, so they are
// left out while the fact that a tool ran is kept.
func TestRenderTranscriptNamesTurnsWithoutToolPayloads(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: "fix the parser"},
		{Role: provider.RoleAssistant, Content: "looking", Reasoning: "secret reasoning", ToolCalls: []provider.ToolCall{{Name: "read_file", Arguments: `{"path":"a.go"}`}}},
		{Role: provider.RoleTool, Name: "read_file", Content: "package main"},
	}
	rendered := RenderTranscript(messages)
	for _, want := range []string{"USER: fix the parser", "ASSISTANT: looking", "ASSISTANT called read_file", "TOOL read_file"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the transcript is missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "secret reasoning") {
		t.Errorf("reasoning should not be in the transcript:\n%s", rendered)
	}
	if strings.Contains(rendered, `"path":"a.go"`) {
		t.Errorf("tool arguments should not be in the transcript:\n%s", rendered)
	}
}

// TestRenderTranscriptStopsAtTheLimit keeps one brief from being as large as
// the history it is meant to replace.
func TestRenderTranscriptStopsAtTheLimit(t *testing.T) {
	messages := make([]provider.Message, 0, 400)
	for i := 0; i < 400; i++ {
		messages = append(messages, provider.Message{Role: provider.RoleUser, Content: strings.Repeat("x", 1000)})
	}
	if got := len(RenderTranscript(messages)); got > transcriptByteLimit {
		t.Errorf("transcript = %d bytes, want at most %d", got, transcriptByteLimit)
	}
}

// TestInsertSummaryReplacesOnlyEarlierTurns is the safety contract: the turn
// being answered stays whole, and the brief keeps the sequence valid.
func TestInsertSummaryReplacesOnlyEarlierTurns(t *testing.T) {
	session := NewSession(t.TempDir(), "m")
	session.AddUser("first request")
	session.AddAssistant("first answer", "", nil)
	session.AddUser("second request")
	session.messages = append(session.messages, provider.Message{Role: provider.RoleAssistant, Content: "second answer"})
	before := len(session.Messages())

	if !session.InsertSummary("the brief") {
		t.Fatalf("InsertSummary should have replaced the earlier turns")
	}
	messages := session.Messages()
	// The brief, then the last user turn and the answer that belongs to it: the
	// current turn is everything from the last user message onward, so only the
	// two messages before it were replaced.
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want the brief plus the two live turn messages", len(messages))
	}
	if messages[0].Role != provider.RoleUser || !strings.Contains(messages[0].Content, "the brief") {
		t.Errorf("the brief should lead as a user turn, got %+v", messages[0])
	}
	if messages[1].Content != "second request" || messages[2].Content != "second answer" {
		t.Errorf("the live turn should be untouched, got %+v", messages[1:])
	}
	if len(messages) >= before {
		t.Errorf("the summary should have shrunk the history")
	}
}

// TestInsertSummaryRefusesWithNothingToReplace keeps a blank or first-turn
// session from gaining a meaningless message.
func TestInsertSummaryRefusesWithNothingToReplace(t *testing.T) {
	session := NewSession(t.TempDir(), "m")
	if session.InsertSummary("brief") {
		t.Errorf("an empty session has nothing to replace")
	}
	session.AddUser("only turn")
	if session.InsertSummary("brief") {
		t.Errorf("the first turn has no earlier turns to replace")
	}
	if got := len(session.Messages()); got != 1 {
		t.Errorf("messages = %d, want the single turn untouched", got)
	}
	session.AddUser("second")
	if session.InsertSummary("   ") {
		t.Errorf("a blank brief must be refused")
	}
}
