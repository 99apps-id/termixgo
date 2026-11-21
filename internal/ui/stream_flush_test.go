package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

func TestCompletedParagraphs(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"one line still typing", ""},
		{"first paragraph\n\nsecond", "first paragraph\n\n"},
		{"first\n\nsecond\n\nthird is typing", "first\n\nsecond\n\n"},
		{"first\n\n", ""},
		{"intro\n\n```go\nx := 1\n\ny := 2", "intro\n\n"},
		{"intro\n\n```go\nx := 1\n\ny := 2\n```\n\nafter", "intro\n\n```go\nx := 1\n\ny := 2\n```\n\n"},
	}
	for _, tc := range cases {
		if got := tc.text[:completedParagraphs(tc.text)]; got != tc.want {
			t.Errorf("%q: printed %q, want %q", tc.text, got, tc.want)
		}
	}
}

// A long answer prints paragraph by paragraph while it streams; the live area
// keeps only the paragraph still being written, and nothing prints twice.
func TestInlineStreamsAnswerParagraphs(t *testing.T) {
	model := inlineModel(t)
	model.blocks = append(model.blocks, block{kind: blockUser, text: "explain"})
	model.running = true
	model.refresh()
	model.pendingPrint = nil

	for _, delta := range []string{"First paragraph about the parser.", "\n\nSecond ", "paragraph", " continues."} {
		model.applyEvent(agent.Event{Kind: agent.EventText, Text: delta})
		model.refresh()
	}
	printed := stripANSI(strings.Join(model.pendingPrint, "\n"))
	if !strings.Contains(printed, "First paragraph about the parser.") || strings.Contains(printed, "Second") {
		t.Fatalf("only the finished paragraph should print:\n%s", printed)
	}
	live := stripANSI(model.View())
	if strings.Contains(live, "First paragraph") || !strings.Contains(live, "Second paragraph continues.") {
		t.Fatalf("the live area holds only the paragraph being written:\n%s", live)
	}

	model.pendingPrint = nil
	model.running = false
	model.refresh()
	rest := stripANSI(strings.Join(model.pendingPrint, "\n"))
	if !strings.Contains(rest, "Second paragraph continues.") || strings.Contains(rest, "First paragraph") {
		t.Fatalf("the end of the turn prints only the rest:\n%s", rest)
	}
}

// A resize prints the streamed answer whole again.
func TestReprintIncludesStreamedParagraphs(t *testing.T) {
	model := inlineModel(t)
	model.running = true
	model.blocks = append(model.blocks, block{kind: blockAssistant, text: "Alpha paragraph.\n\nBeta paragraph", streamed: len("Alpha paragraph.\n\n")})
	model.printed = len(model.blocks) - 1
	model.reprintAll()
	if model.blocks[len(model.blocks)-1].streamed != len("Alpha paragraph.\n\n") {
		t.Fatalf("the reprint should print the finished paragraph again, streamed = %d", model.blocks[len(model.blocks)-1].streamed)
	}
}
