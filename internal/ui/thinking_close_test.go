package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
)

// Reasoning closes, with its own duration, as soon as the answer starts, so
// the inline layout prints it right away; the late EventReasoned refines the
// same block instead of adding a second one.
func TestThinkingClosesWhenTheAnswerStarts(t *testing.T) {
	model := inlineModel(t)
	model.blocks = append(model.blocks, block{kind: blockUser, text: "why"})
	model.running = true
	model.refresh()
	model.pendingPrint = nil

	model.applyEvent(agent.Event{Kind: agent.EventThinking, Text: "weighing it"})
	model.lastBlock().started = time.Now().Add(-3 * time.Second)
	model.applyEvent(agent.Event{Kind: agent.EventText, Text: "Because"})
	model.refresh()

	thinking := model.blocks[len(model.blocks)-2]
	if thinking.running || !thinking.finalized || thinking.seconds != 3 {
		t.Fatalf("the reasoning should close with its own duration: %+v", thinking)
	}
	printed := stripANSI(strings.Join(model.pendingPrint, "\n"))
	if !strings.Contains(printed, "∴ Thought for 3s") {
		t.Fatalf("the closed reasoning should print at once:\n%s", printed)
	}

	model.applyEvent(agent.Event{Kind: agent.EventReasoned, Text: "weighing it fully", ToolMillis: 4200})
	count := 0
	for _, item := range model.blocks {
		if item.kind == blockThinking {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the late event must update the same block, got %d thinking blocks", count)
	}
	if updated := model.blocks[len(model.blocks)-2]; updated.seconds != 4 || !updated.reasoned {
		t.Fatalf("the late event should refine the duration: %+v", updated)
	}
}

// A late event never lands on an earlier turn's reasoning.
func TestLateReasonedStaysInItsTurn(t *testing.T) {
	model := chatModel(t)
	model.blocks = append(model.blocks,
		block{kind: blockUser, text: "first"},
		block{kind: blockThinking, finalized: true, reasoning: "old"},
		block{kind: blockUser, text: "second"},
	)
	model.applyEvent(agent.Event{Kind: agent.EventReasoned, Text: "new reasoning", ToolMillis: 2000})
	if model.blocks[len(model.blocks)-3].reasoning != "old" {
		t.Fatalf("the earlier turn's reasoning was overwritten")
	}
	if last := model.lastBlock(); last.kind != blockThinking || last.reasoning != "new reasoning" {
		t.Fatalf("the reasoning should be added to this turn: %+v", last)
	}
}
