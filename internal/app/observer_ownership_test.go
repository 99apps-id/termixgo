package app

import (
	"context"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

// TestALosingCallerCannotDetachALiveTurnsObserver is the observer ownership
// guard.
//
// The observer was one global slot. A second surface - an inbound Telegram
// message, or a scheduled job firing during a live turn - installed its own,
// lost the single-run race, and cleared the slot on the way out. The turn that
// was still streaming then had no sink at all: the chat sat on "Working...",
// progress stopped arriving, and the prompt handed back an empty answer because
// the error notice went nowhere.
func TestALosingCallerCannotDetachALiveTurnsObserver(t *testing.T) {
	application := newTestApp(t)

	seen := 0
	claim := application.SetObserver(func(agent.Event) { seen++ })
	if claim == 0 {
		t.Fatalf("the first caller should be granted the slot")
	}

	// A caller that does not own the turn runs the same path and releases on
	// the way out. There is no model configured, so its turn is refused.
	if _, err := application.RunPrompt(context.Background(), "hello", func(string) {}); err == nil {
		t.Fatalf("expected the turn to fail with no model configured")
	}

	baseline := seen
	application.emit(agent.Event{Kind: agent.EventNotice, Text: "still working"})
	if seen != baseline+1 {
		t.Errorf("the live turn's observer saw %d new events, want 1: a caller that never owned the slot detached it", seen-baseline)
	}

	// The holder can still release its own claim, and then nothing streams.
	application.ClearObserver(claim)
	baseline = seen
	application.emit(agent.Event{Kind: agent.EventNotice, Text: "after release"})
	if seen != baseline {
		t.Errorf("the observer kept running after its holder released the slot")
	}
}

// TestASecondClaimDoesNotDisplaceTheHolder is the other half: claiming while the
// slot is held must not silently re-point another turn's stream.
func TestASecondClaimDoesNotDisplaceTheHolder(t *testing.T) {
	application := newTestApp(t)

	held := 0
	claim := application.SetObserver(func(agent.Event) { held++ })
	if claim == 0 {
		t.Fatalf("the first caller should be granted the slot")
	}

	if second := application.SetObserver(func(agent.Event) {
		t.Errorf("a displaced observer must not receive events")
	}); second != 0 {
		t.Errorf("a claim was granted while another turn held the slot")
	}

	application.emit(agent.Event{Kind: agent.EventNotice, Text: "one"})
	if held != 1 {
		t.Errorf("the holder saw %d events, want 1", held)
	}

	// Releasing a claim that was never granted is a no-op, so the holder is
	// untouched by the caller that was refused.
	application.ClearObserver(0)
	application.emit(agent.Event{Kind: agent.EventNotice, Text: "two"})
	if held != 2 {
		t.Errorf("the holder saw %d events, want 2: a refused caller cleared the slot", held)
	}
}
