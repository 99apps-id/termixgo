package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestCancelledContextStopsTheTurn is what the Ctrl+C handler relies on. The
// signal handler only cancels a context, so this asserts that cancelling is
// enough to end a turn promptly rather than leaving it running.
func TestCancelledContextStopsTheTurn(t *testing.T) {
	release := make(chan struct{})
	application, server := readyAppHeld(t, release)
	defer func() {
		close(release)
		server.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())

	finished := make(chan error, 1)
	go func() { finished <- application.RunTurn(ctx, "start something long") }()

	// Wait until the turn is genuinely in flight before cancelling.
	deadline := time.Now().Add(5 * time.Second)
	for !application.Running() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !application.Running() {
		t.Fatalf("the turn never started")
	}

	cancel()

	select {
	case err := <-finished:
		// A cancelled turn reports no error: stopping is a normal outcome,
		// and the UI shows it as "Stopped." rather than a failure.
		if err != nil {
			t.Errorf("a cancelled turn returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("cancelling the context did not end the turn")
	}

	if application.Running() {
		t.Errorf("the app still reports running after cancellation")
	}
	// The run slot must be released, or the next prompt would be refused.
	// This is checked directly rather than by starting another turn, because
	// the held server would make that second turn block too.
	if !application.runMu.TryLock() {
		t.Errorf("the run slot was not released after cancellation")
	} else {
		application.runMu.Unlock()
	}
}

// TestEphemeralFlagDoesNotChangeRunErrors keeps the two concerns separate:
// suppressing session files must not swallow a real failure.
func TestEphemeralFlagDoesNotChangeRunErrors(t *testing.T) {
	application := newTestApp(t)
	application.SetEphemeral(true)

	err := application.RunTurn(context.Background(), "hello")
	if !errors.Is(err, ErrNoModel) {
		t.Errorf("err = %v, want ErrNoModel even when ephemeral", err)
	}
}
