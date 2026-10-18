package ui

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

// TestSteerMessageAppearsOnce is the immediate-feedback guard: pressing Enter
// during a run shows the operator's message as a user block at once, and the
// agent's later "Steering:" notice for the same text is dropped so it is not
// shown twice.
func TestSteerMessageAppearsOnce(t *testing.T) {
	model := chatModel(t)
	before := len(model.blocks)

	model.recordSteer("fix the parser")
	if len(model.blocks) != before+1 {
		t.Fatalf("the steer should add one block, got %d new", len(model.blocks)-before)
	}
	last := model.blocks[len(model.blocks)-1]
	if last.kind != blockUser || last.text != "fix the parser" {
		t.Fatalf("the steer block = %+v, want a user block with the text", last)
	}

	// The agent folds the steer in and emits its notice; the echo made that
	// notice redundant.
	model.applyEvent(agent.Event{Kind: agent.EventNotice, Text: "Steering: fix the parser"})
	if len(model.blocks) != before+1 {
		t.Errorf("the echoed steer notice should be dropped, got %d blocks", len(model.blocks))
	}

	// A steer notice the UI did not echo still appears.
	model.applyEvent(agent.Event{Kind: agent.EventNotice, Text: "Steering: something else"})
	if got := model.blocks[len(model.blocks)-1]; got.kind != blockNotice {
		t.Errorf("an un-echoed steer notice should appear, got %+v", got)
	}
}
