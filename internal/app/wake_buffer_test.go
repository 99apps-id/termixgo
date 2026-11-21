package app

import (
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
)

// A wake is not a display event: with the buffer full of text deltas it waits
// for room instead of being dropped, so a finished background subagent still
// reaches the model.
func TestWakeIsNotDroppedWhenTheBufferIsFull(t *testing.T) {
	application := newTestApp(t)
	for len(application.events) < cap(application.events) {
		application.events <- agent.Event{Kind: agent.EventText, Text: "x"}
	}
	done := make(chan struct{})
	go func() {
		application.emit(agent.Event{Kind: agent.EventWake, Text: "agent-1 finished"})
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		t.Fatalf("the wake should be waiting for room, not already dropped")
	default:
	}
	delivered := false
	deadline := time.After(3 * time.Second)
	for !delivered {
		select {
		case event := <-application.events:
			delivered = event.Kind == agent.EventWake
		case <-deadline:
			t.Fatalf("the wake never arrived")
		}
	}
	<-done
}

// A display event is still dropped rather than blocking the run.
func TestDisplayEventsStillDropWhenTheBufferIsFull(t *testing.T) {
	application := newTestApp(t)
	for len(application.events) < cap(application.events) {
		application.events <- agent.Event{Kind: agent.EventText, Text: "x"}
	}
	finished := make(chan struct{})
	go func() {
		application.emit(agent.Event{Kind: agent.EventText, Text: "late"})
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatalf("a display event must never block the run")
	}
}
