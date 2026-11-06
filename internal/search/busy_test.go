package search

import (
	"testing"
	"time"
)

// Two processes share `.termixgo/search.db`: `termixgo serve` indexes while the
// CLI runs `search_memory`. With busy_timeout at its default of 0 the second
// writer gives up the instant the first holds the write lock, so an index
// refresh from one process made a search in the other fail with "database is
// locked". The writer must wait for the lock to clear instead.
func TestConcurrentWriterWaitsForTheLockInsteadOfFailing(t *testing.T) {
	dir := t.TempDir()
	holder, err := OpenIn(dir, dir)
	if err != nil {
		t.Fatalf("OpenIn(holder): %v", err)
	}
	defer holder.Close()
	waiter, err := OpenIn(dir, dir)
	if err != nil {
		t.Fatalf("OpenIn(waiter): %v", err)
	}
	defer waiter.Close()

	// A write transaction holds the single write lock between processes.
	tx, err := holder.db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO docs(scope, path, title, body) VALUES('probe','held','held','held')`); err != nil {
		t.Fatalf("hold write lock: %v", err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(200 * time.Millisecond)
		_ = tx.Commit()
	}()

	start := time.Now()
	indexErr := waiter.Index("workspace", "second.go", "second.go", "package second")
	waited := time.Since(start)
	<-released

	if indexErr != nil {
		t.Fatalf("second writer failed instead of waiting for the lock: %v (after %s)", indexErr, waited)
	}
	if waited < 100*time.Millisecond {
		t.Fatalf("second writer did not wait for the lock (returned in %s)", waited)
	}
}
