package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// The largest single tool result the agent can store: read_file caps a window
// at maxReadBytes and run_command caps output at maxOutputChars.
const largestToolResult = 64 * 1024

func toolTurn(id string, size int) []provider.Message {
	return []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: id, Name: "read_file", Arguments: `{}`}}},
		{Role: provider.RoleTool, ToolID: id, Name: "read_file", Content: strings.Repeat("x", size)},
	}
}

// TestCompactFitsWhateverTheTailHolds is the audit repro and its generalisation.
//
// Compaction trimmed as little as it could and stopped. The newest four messages
// were beyond reach entirely, so one large recent tool result left the request
// over the model's window for the rest of the session: every later step was
// rejected by the provider, the failure looked unrelated to anything the
// operator asked, and only /new cleared it.
func TestCompactFitsWhateverTheTailHolds(t *testing.T) {
	tests := []struct {
		name   string
		build  func() []provider.Message
		budget int
	}{
		{
			name: "one read_file at the tail of a short turn",
			build: func() []provider.Message {
				return append([]provider.Message{
					{Role: provider.RoleUser, Content: "read the log"},
				}, toolTurn("c1", largestToolResult)...)
			},
			budget: HistoryBudget(32000),
		},
		{
			name: "several max-sized results in one turn",
			build: func() []provider.Message {
				messages := []provider.Message{{Role: provider.RoleUser, Content: "read them all"}}
				for index := 0; index < 6; index++ {
					messages = append(messages, toolTurn(fmt.Sprintf("c%d", index), largestToolResult)...)
				}
				return messages
			},
			budget: HistoryBudget(32000),
		},
		{
			name: "a small local window with a long history",
			build: func() []provider.Message {
				messages := []provider.Message{{Role: provider.RoleUser, Content: "start"}}
				for index := 0; index < 60; index++ {
					messages = append(messages,
						provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("a", 900)},
						provider.Message{Role: provider.RoleUser, Content: strings.Repeat("u", 900)},
					)
				}
				return messages
			},
			budget: HistoryBudget(8192),
		},
		{
			name: "the budget of one oversized message",
			build: func() []provider.Message {
				return append([]provider.Message{
					{Role: provider.RoleUser, Content: "read it"},
				}, toolTurn("c1", largestToolResult)...)
			},
			budget: 1000,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			messages := test.build()
			if EstimateMessages(messages) <= test.budget {
				t.Fatalf("the fixture is already inside its budget: %d <= %d", EstimateMessages(messages), test.budget)
			}
			trimmed := Compact(messages, test.budget)
			if len(trimmed) == 0 {
				t.Fatalf("compaction erased the conversation instead of trimming it")
			}
			if used := EstimateMessages(trimmed); used > test.budget {
				t.Errorf("the request is still over budget: %d used, %d allowed (over by %d)",
					used, test.budget, used-test.budget)
			}
			if trimmed[0].Role == provider.RoleTool {
				t.Errorf("a compacted request must not begin with a tool result")
			}
		})
	}
}

// TestCompactNeverMutatesTheSourceThatWasPassedIn guards the copy rules on both
// passes. hardFit reuses the slice Compact already copied, and writes Content
// on messages it owns; elide clones the tool-call slice before blanking
// arguments. Either path writing through to the caller would rewrite the
// session's own history, so an old call's arguments would be lost from the
// session file, not just from the request being trimmed.
func TestCompactNeverMutatesTheSourceThatWasPassedIn(t *testing.T) {
	messages := append([]provider.Message{
		{Role: provider.RoleUser, Content: "read it"},
	}, toolTurn("c1", largestToolResult)...)
	messages[1].ToolCalls = []provider.ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"big.log"}`}}

	trimmed := Compact(messages, 1000)
	if len(trimmed) == 0 {
		t.Fatal("compaction erased the conversation")
	}
	if used := EstimateMessages(trimmed); used > 1000 {
		t.Errorf("the request is still over budget: %d > 1000", used)
	}
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if call.Arguments != `{"path":"big.log"}` {
				t.Fatalf("Compact rewrote the caller's tool arguments: %q", call.Arguments)
			}
		}
	}
}

// TestCompactNeverEmitsAnEmptyAssistantTurn is the empty-content 400.
//
// The share pass cuts text only. When an assistant turn's share ran out before
// its first byte, its content became the empty string while it carried no tool
// call, and the encoder turns blank content into null. The provider then rejects
// the whole request with "content or toolcalls must be set", which surfaced as
// Failed to contact the AI model on every later step of that session.
func TestCompactNeverEmitsAnEmptyAssistantTurn(t *testing.T) {
	// A window far below what the fixed overhead of these messages needs, so
	// every share collapses to zero and the pass blanks what it can.
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: strings.Repeat("u", 400)},
		{Role: provider.RoleAssistant, Content: strings.Repeat("a", 400)},
		{Role: provider.RoleUser, Content: strings.Repeat("v", 400)},
		{Role: provider.RoleAssistant, Content: strings.Repeat("b", 400)},
	}
	trimmed := Compact(messages, 12)

	for index, message := range trimmed {
		if message.Role != provider.RoleAssistant {
			continue
		}
		if message.Content == "" && len(message.ToolCalls) == 0 && len(message.Images) == 0 {
			t.Fatalf("message %d is an assistant turn with neither content nor tool calls, "+
				"which the provider rejects: %#v", index, message)
		}
	}
}

// TestCompactLeavesAFittingConversationAlone keeps the guarantee from becoming
// a habit of touching work that did not need it.
func TestCompactLeavesAFittingConversationAlone(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: "is this fine"},
		{Role: provider.RoleAssistant, Content: "yes"},
	}
	trimmed := Compact(messages, 5000)
	if len(trimmed) != len(messages) {
		t.Fatalf("a conversation inside its budget must not be reshaped")
	}
	for index := range messages {
		if trimmed[index].Content != messages[index].Content {
			t.Errorf("message %d was rewritten: %q", index, trimmed[index].Content)
		}
	}
}
