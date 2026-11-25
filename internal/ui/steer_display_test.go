package ui

import (
	"os"
	"path/filepath"
	"strings"
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

// A background wake is shown as its headline. The agent's steering notice for it
// must not repeat the raw event markup.
func TestWakeSteerShowsOnlyItsHeadline(t *testing.T) {
	model := chatModel(t)
	model.running = true
	updated, _ := model.handleWake(agent.Event{Kind: agent.EventWake, Text: "worker done"})
	model = updated.(*Model)
	before := len(model.blocks)
	for _, steer := range model.app.TakeSteer() {
		model.applyEvent(agent.Event{Kind: agent.EventNotice, Text: "Steering: " + steer})
	}
	if len(model.blocks) != before {
		t.Errorf("the wake appeared twice: %+v", model.blocks[len(model.blocks)-1])
	}
}

// A notice the event queue dropped must not make every later steer show twice.
func TestSteerEchoSurvivesADroppedNotice(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.recordSteer("first")
	model.recordSteer("second")
	before := len(model.blocks)
	model.applyEvent(agent.Event{Kind: agent.EventNotice, Text: "Steering: second"})
	if len(model.blocks) != before {
		t.Errorf("an echoed steer was repeated: %+v", model.blocks[len(model.blocks)-1])
	}
}

// An @file in a steer attaches the file, as it does in a prompt.
func TestSteerExpandsFileMentions(t *testing.T) {
	model := chatModel(t)
	model.running = true
	path := filepath.Join(model.app.Workspace(), "steer-note.txt")
	if err := os.WriteFile(path, []byte("use the new parser"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	model.recordSteer("follow @steer-note.txt")
	got := model.app.TakeSteer()
	if len(got) != 1 || !strings.Contains(got[0], "use the new parser") {
		t.Errorf("steer = %q, want the file attached", got)
	}
}
