package ui

import (
	"strings"
	"testing"
)

// TestReconcileAnswerTextRestoresDroppedDeltas pins the repair for a lost text
// delta: the event queue is bounded, so under load the transcript can be left
// with a prefix of the answer. The stored session message is authoritative.
func TestReconcileAnswerTextRestoresDroppedDeltas(t *testing.T) {
	model := chatModel(t)
	session := model.app.Session()

	model.blocks = append(model.blocks, block{kind: blockUser, text: "fix the build"})
	model.turnMark = session.MessageCount()
	model.blocks = append(model.blocks, block{kind: blockAssistant, text: "The build"})
	session.AddAssistant("The build passes after the fix.", "", nil, nil)

	model.reconcileAnswerText()

	last := model.blocks[len(model.blocks)-1]
	if last.kind != blockAssistant {
		t.Fatalf("last block = kind %d, want assistant", last.kind)
	}
	if last.text != "The build passes after the fix." {
		t.Errorf("answer = %q, want the stored whole answer", last.text)
	}
}

// TestReconcileAnswerTextRestoresAWhollyDroppedAnswer covers the worst case:
// every delta was dropped, so no assistant block exists and the reply has to
// be appended rather than left invisible.
func TestReconcileAnswerTextRestoresAWhollyDroppedAnswer(t *testing.T) {
	model := chatModel(t)
	session := model.app.Session()

	model.blocks = append(model.blocks, block{kind: blockUser, text: "explain"})
	model.turnMark = session.MessageCount()
	session.AddAssistant("Here is the whole explanation.", "", nil, nil)

	model.reconcileAnswerText()

	last := model.blocks[len(model.blocks)-1]
	if last.kind != blockAssistant || !strings.Contains(last.text, "whole explanation") {
		t.Fatalf("a dropped answer was not restored: kind %d, text %q", last.kind, last.text)
	}
}

// TestReconcileAnswerTextLeavesEarlierTurnsAlone pins the turn boundary: a run
// that produced no answer must not rewrite the previous turn's reply.
func TestReconcileAnswerTextLeavesEarlierTurnsAlone(t *testing.T) {
	model := chatModel(t)
	session := model.app.Session()

	session.AddUser("first prompt")
	session.AddAssistant("the previous answer", "", nil, nil)
	base := len(model.blocks)
	model.blocks = append(model.blocks,
		block{kind: blockUser, text: "first prompt"},
		block{kind: blockAssistant, text: "the previous answer"},
	)
	// The turn's own user message lands after the mark, exactly as RunTurn
	// adds it, and the turn then produces no answer.
	model.turnMark = session.MessageCount()
	session.AddUser("second prompt")
	model.blocks = append(model.blocks, block{kind: blockUser, text: "second prompt"})

	model.reconcileAnswerText()

	if len(model.blocks) != base+3 {
		t.Fatalf("an answerless turn appended %d blocks", len(model.blocks)-(base+3))
	}
	if model.blocks[base+1].text != "the previous answer" {
		t.Errorf("previous answer rewritten to %q", model.blocks[base+1].text)
	}
}
