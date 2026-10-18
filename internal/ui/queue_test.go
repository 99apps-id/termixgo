package ui

import (
	"strings"
	"testing"
)

// TestQueueDrainsOnRunDone proves the steer contract: an input typed while
// busy runs right after the current turn instead of being lost.
func TestQueueDrainsOnRunDone(t *testing.T) {
	model := chatModel(t)
	model.running = true
	// /new starts work, so it waits for the drain. A read-only command such as
	// /status runs at once during a turn and would not exercise the queue.
	model.composer.SetValue("/new")

	queued := press(t, model, "enter")
	if len(queued.queue) != 1 {
		t.Fatalf("queue = %d, want 1 held input", len(queued.queue))
	}

	drained, _ := send(t, queued, runDoneMsg{})
	if len(drained.queue) != 0 {
		t.Errorf("queue = %d, want the held input started", len(drained.queue))
	}
	if drained.running {
		t.Errorf("a slash follow-up must not leave a run behind")
	}
	if view := display(drained); !strings.Contains(view, "Started a new session") {
		t.Errorf("the queued /new should have run:\n%s", view)
	}
}

// TestQueueHoldsOrderAcrossTurns queues two follow-ups and shows each
// runDone starts exactly one, so steers run in the order typed.
func TestQueueHoldsOrderAcrossTurns(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.composer.SetValue("/new")
	model = press(t, model, "enter")
	model.composer.SetValue("/telegram status")
	model = press(t, model, "enter")
	if len(model.queue) != 2 {
		t.Fatalf("queue = %d, want 2 held inputs", len(model.queue))
	}

	first, _ := send(t, model, runDoneMsg{})
	if len(first.queue) != 1 || first.queue[0] != "/telegram status" {
		t.Fatalf("queue = %q, want only the second input left", first.queue)
	}

	second, _ := send(t, first, runDoneMsg{})
	if len(second.queue) != 0 {
		t.Errorf("queue = %d, want every held input started", len(second.queue))
	}
	if view := display(second); !strings.Contains(view, "Started a new session") || !strings.Contains(view, "Telegram:") {
		t.Errorf("both queued commands should have run:\n%s", view)
	}
}

// TestQueueIsBounded keeps a held Enter key from growing memory without
// limit on a small machine.
func TestQueueIsBounded(t *testing.T) {
	model := chatModel(t)
	model.running = true
	for i := 0; i < maxQueuedInputs; i++ {
		model.composer.SetValue("/new")
		model = press(t, model, "enter")
	}
	if len(model.queue) != maxQueuedInputs {
		t.Fatalf("queue = %d, want %d", len(model.queue), maxQueuedInputs)
	}

	model.composer.SetValue("/new")
	full := press(t, model, "enter")
	if len(full.queue) != maxQueuedInputs {
		t.Errorf("a full queue must refuse more input, got %d", len(full.queue))
	}
	if !strings.Contains(full.notice, "full") {
		t.Errorf("the refusal should say the queue is full, got %q", full.notice)
	}
}

// TestEscWhileRunningKeepsTheQueue stops the turn without dropping an input
// the operator already typed.
//
// /new is the input under test rather than /status: /status now runs at once
// during a turn, so it never reaches the queue. /new starts work, so it waits.
func TestEscWhileRunningKeepsTheQueue(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.composer.SetValue("/new")
	model = press(t, model, "enter")

	stopped := press(t, model, "esc")
	if len(stopped.queue) != 1 {
		t.Errorf("queue = %d, want the input kept across stop", len(stopped.queue))
	}
}

// TestQueuedCountIsVisible surfaces the queue in the status bar and hints,
// so the operator can see that something is waiting.
func TestQueuedCountIsVisible(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.composer.SetValue("/new")
	model = press(t, model, "enter")

	if view := display(model); !strings.Contains(view, "queue 1") {
		t.Errorf("the status bar should show the queue:\n%s", view)
	}
	if !strings.Contains(model.viewHints(), "1 queued") {
		t.Errorf("the hints should name the queued input, got %q", model.viewHints())
	}
}
