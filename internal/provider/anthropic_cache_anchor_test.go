package provider

import (
	"strings"
	"testing"
)

// cacheBreakpoints counts every ephemeral marker anywhere in one message's
// content, so a test can tell where the breakpoints actually land.
func cacheBreakpoints(message map[string]any) int {
	switch content := message["content"].(type) {
	case string:
		return 0
	case []map[string]any:
		count := 0
		for _, block := range content {
			if _, ok := block["cache_control"]; ok {
				count++
			}
		}
		return count
	}
	return 0
}

func cacheMessages(turns int) []map[string]any {
	messages := make([]map[string]any, 0, turns)
	for i := 0; i < turns; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, map[string]any{"role": role, "content": "turn text"})
	}
	return messages
}

// TestPromptCacheAnchorStaysAtTheHead is the reason this function changed.
//
// Marking only the second-to-last turn moved the cached prefix on every turn:
// each new turn rewrote the region the previous turn had just cached, so a long
// conversation paid full input price for its own history every time. The first
// message never changes once written, so an anchor there stays warm.
func TestPromptCacheAnchorStaysAtTheHead(t *testing.T) {
	short := cacheMessages(4)
	applyPromptCacheToMessages(short)
	if got := cacheBreakpoints(short[0]); got != 1 {
		t.Fatalf("the first message should carry the stable anchor, got %d breakpoints", got)
	}

	// The same conversation plus two more turns: the anchor must still be on
	// the first message, at the same place, or the prefix was rewritten.
	longer := cacheMessages(6)
	applyPromptCacheToMessages(longer)
	if got := cacheBreakpoints(longer[0]); got != 1 {
		t.Fatalf("the anchor moved when the conversation grew, got %d breakpoints on the head", got)
	}
	if longer[0]["content"].([]map[string]any)[0]["text"] != "turn text" {
		t.Errorf("the anchor rewrote the first message's content")
	}
}

// TestPromptCacheStaysWithinTheBreakpointLimit guards a hard API limit: the
// system block and the last tool already take two of Anthropic's four, so the
// conversation may add no more than two of its own.
func TestPromptCacheStaysWithinTheBreakpointLimit(t *testing.T) {
	for _, turns := range []int{1, 2, 3, 4, 12, 40} {
		messages := cacheMessages(turns)
		applyPromptCacheToMessages(messages)
		total := 0
		for _, message := range messages {
			total += cacheBreakpoints(message)
		}
		if total > 2 {
			t.Errorf("%d turns produced %d conversation breakpoints, want at most 2", turns, total)
		}
	}
}

// TestPromptCacheMarksTheTailToo keeps the second half of the design: the tail
// marker is what extends the cached prefix over the newest turns.
func TestPromptCacheMarksTheTailToo(t *testing.T) {
	messages := cacheMessages(6)
	applyPromptCacheToMessages(messages)
	if got := cacheBreakpoints(messages[4]); got != 1 {
		t.Errorf("the second-to-last message should carry the moving marker, got %d", got)
	}
}

// TestPromptCacheIgnoresBlankContent keeps a marker from being attached to a
// message that carries nothing: rewriting a blank string into a block would add
// noise to the request for no cached bytes.
func TestPromptCacheIgnoresBlankContent(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": "   "},
		{"role": "assistant", "content": "answer"},
	}
	applyPromptCacheToMessages(messages)
	if _, rewritten := messages[0]["content"].([]map[string]any); rewritten {
		t.Errorf("a blank head message should not be rewritten into blocks")
	}
}

// TestPromptCacheHandlesBlockContent covers the tool-use shape: an assistant
// turn with tool calls arrives as blocks, and the marker belongs on the last
// one so the call arguments are inside the cached prefix.
func TestPromptCacheHandlesBlockContent(t *testing.T) {
	messages := []map[string]any{
		{
			"role": "assistant",
			"content": []map[string]any{
				{"type": "text", "text": "let me look"},
				{"type": "tool_use", "id": "t1", "name": "read_file", "input": map[string]any{}},
			},
		},
		{"role": "user", "content": "go on"},
	}
	applyPromptCacheToMessages(messages)
	blocks := messages[0]["content"].([]map[string]any)
	if _, ok := blocks[len(blocks)-1]["cache_control"]; !ok {
		t.Errorf("the marker should sit on the last block, got %#v", blocks)
	}
	if strings.TrimSpace(blocks[1]["type"].(string)) != "tool_use" {
		t.Errorf("the cached block should be the tool call, got %v", blocks[1]["type"])
	}
}

// TestPromptCacheEmptyConversationDoesNothing is the guard on the index
// arithmetic: an empty message list must not index out of range.
func TestPromptCacheEmptyConversationDoesNothing(t *testing.T) {
	applyPromptCacheToMessages(nil)
	applyPromptCacheToMessages([]map[string]any{})
}
