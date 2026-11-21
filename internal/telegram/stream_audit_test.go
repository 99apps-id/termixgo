package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/channel"
)

// A silent answer after streamed overflow must not leave the live draft, the
// message ending in the cursor, in the chat.
func TestSilentAnswerAfterOverflowDeletesTheDraft(t *testing.T) {
	prev := streamEditInterval
	streamEditInterval = 10 * time.Millisecond
	t.Cleanup(func() { streamEditInterval = prev })

	api := newRecordingAPI(t)
	long := strings.Repeat("a streamed line of plain text\n", 160)
	agent := &scriptedStreamAgent{
		scriptedAgent: scriptedAgent{model: "test-model"},
		streamFn: func(ctx context.Context, req channel.Request, prompt string, onEvent func(StreamEvent)) (string, error) {
			onEvent(StreamEvent{Kind: "text", Text: long})
			time.Sleep(60 * time.Millisecond)
			return "HEARTBEAT_OK", nil
		},
	}
	bot, _ := pairedBot(t, agent)
	bot.client = api.client()
	bot.handleMessage(context.Background(), message("go"))

	if len(api.calls("sendMessage")) < 2 {
		t.Fatalf("the long draft should have overflowed into a second message")
	}
	if len(api.calls("deleteMessage")) != 1 {
		t.Fatalf("the live draft should be deleted, deletes = %d", len(api.calls("deleteMessage")))
	}
}

func TestToolCardUsesNoEmoji(t *testing.T) {
	for _, mark := range []string{toolRunningMark, toolDoneMark, toolFailedMark} {
		for _, r := range mark {
			if r >= 0x1F000 || (r >= 0x2300 && r <= 0x23FF) {
				t.Fatalf("mark %q carries an emoji", mark)
			}
		}
	}
}

func TestCapCardLinesCarriesTheDroppedCount(t *testing.T) {
	lines := []string{"aaaa", "bbbb", "cccc"}
	lines = capCardLines(lines, 9)
	if lines[0] != "... (1 earlier lines dropped)" {
		t.Fatalf("first cap = %q", lines)
	}
	lines = capCardLines(append(lines, "dddd"), 9)
	if lines[0] != "... (2 earlier lines dropped)" || lines[len(lines)-1] != "dddd" {
		t.Fatalf("second cap should carry the count: %q", lines)
	}
}
