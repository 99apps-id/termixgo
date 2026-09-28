package app

import (
	"context"
	"testing"
	"time"
)

// TestRunTurnDoesNotDeadlock completes quickly or not at all.
//
// This exists because a locking mistake once made every run hang: the turn path
// held the app mutex and then called an accessor that took it again. Go mutexes
// are not reentrant, so the whole app froze. The test suite only noticed through
// its ten minute timeout, which is far too slow to be useful, so this bounds the
// wait and reports a hang as the failure it is.
func TestRunTurnDoesNotDeadlock(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()

	done := make(chan error, 1)
	go func() { done <- application.RunTurn(context.Background(), "hello") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a turn against a working provider failed: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("a turn did not finish in 20s: the run path is deadlocked or wedged")
	}
}

// TestConcurrentReadsAreNotBlockedByATurn checks that the accessors a UI calls
// while a turn runs do not block on work the turn is doing.
//
// The terminal polls Status, Session and Usage while the agent is busy, so a
// slow or re-entrant lock there would freeze the display, not just a test.
func TestConcurrentReadsAreNotBlockedByATurn(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()

	finished := make(chan error, 1)
	go func() { finished <- application.RunTurn(context.Background(), "hello") }()

	// Hammer the read paths while the turn is in flight.
	readsDone := make(chan struct{})
	go func() {
		defer close(readsDone)
		for index := 0; index < 200; index++ {
			_ = application.Status()
			_ = application.Session().ID
			_ = application.Usage()
			_ = application.ModelLabel()
			_, _ = application.Cost()
			_ = application.ContextUsage()
		}
	}()

	select {
	case <-readsDone:
	case <-time.After(20 * time.Second):
		t.Fatalf("a read blocked while a turn was running")
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("the turn failed: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("the turn did not finish")
	}
}
