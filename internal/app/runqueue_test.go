package app

import (
	"context"
	"sync"
	"testing"
	"time"
)

// waitForWaiters blocks until the queue holds at least count waiters. It reads
// the queue's own lock, so the test observes the real state rather than a
// sleep.
func waitForWaiters(t *testing.T, queue *runQueue, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if queue.Waiting() >= count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("queue never reached %d waiters (held %d)", count, queue.Waiting())
}

func TestRunQueueServesHighestPriorityFirst(t *testing.T) {
	queue := newRunQueue()
	ctx := context.Background()
	if err := queue.acquire(ctx, PriorityOperator); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	order := make(chan RunPriority, 2)
	start := func(priority RunPriority) {
		go func() {
			if err := queue.acquire(ctx, priority); err != nil {
				return
			}
			order <- priority
			queue.release()
		}()
	}
	start(PriorityScheduled)
	waitForWaiters(t, queue, 1)
	start(PriorityChannel)
	waitForWaiters(t, queue, 2)

	queue.release()

	first := <-order
	if first != PriorityChannel {
		t.Fatalf("first granted = %d, want the channel priority", first)
	}
	second := <-order
	if second != PriorityScheduled {
		t.Fatalf("second granted = %d, want the scheduled priority", second)
	}
}

func TestRunQueueCancelledWaiterLeaves(t *testing.T) {
	queue := newRunQueue()
	if err := queue.acquire(context.Background(), PriorityOperator); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	waitCtx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() { errs <- queue.acquire(waitCtx, PriorityChannel) }()
	waitForWaiters(t, queue, 1)
	cancel()

	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("a cancelled waiter must not be granted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled waiter stayed blocked")
	}
	if queue.Waiting() != 0 {
		t.Fatalf("waiting = %d after cancellation", queue.Waiting())
	}
	queue.release()
}

func TestRunQueueHeartbeatNeverWaits(t *testing.T) {
	queue := newRunQueue()
	if !queue.tryAcquire(PriorityHeartbeat) {
		t.Fatal("an idle queue must grant the heartbeat")
	}
	if queue.tryAcquire(PriorityScheduled) {
		t.Fatal("a busy queue must refuse the heartbeat path")
	}
	queue.release()

	// A queued waiter also refuses the heartbeat, so it cannot jump ahead.
	acquired := make(chan struct{})
	done := make(chan struct{})
	go func() {
		if err := queue.acquire(context.Background(), PriorityOperator); err == nil {
			close(acquired)
			<-done
			queue.release()
		}
	}()
	<-acquired
	if queue.tryAcquire(PriorityHeartbeat) {
		t.Fatal("a heartbeat must not take a busy slot")
	}
	close(done)
}

func TestRunQueueReleaseHandsOff(t *testing.T) {
	queue := newRunQueue()
	ctx := context.Background()
	if err := queue.acquire(ctx, PriorityOperator); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	granted := make(chan struct{})
	go func() {
		defer wg.Done()
		if err := queue.acquire(ctx, PriorityChannel); err != nil {
			return
		}
		close(granted)
		queue.release()
	}()
	waitForWaiters(t, queue, 1)
	queue.release()
	select {
	case <-granted:
	case <-time.After(2 * time.Second):
		t.Fatal("release did not hand the slot to the waiter")
	}
	wg.Wait()
	if _, busy := queue.Holder(); busy {
		t.Fatal("the queue must be idle at the end")
	}
}

func TestRunQueueCloseWakesWaiters(t *testing.T) {
	queue := newRunQueue()
	ctx := context.Background()
	if err := queue.acquire(ctx, PriorityOperator); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	errs := make(chan error, 1)
	go func() { errs <- queue.acquire(ctx, PriorityChannel) }()
	waitForWaiters(t, queue, 1)
	queue.close()
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("a closed queue must refuse the waiter")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not wake the waiter")
	}
	queue.release()
}

func TestRunWaitTimeouts(t *testing.T) {
	if runWaitTimeout(PriorityHeartbeat) != 0 {
		t.Fatal("the heartbeat must not wait")
	}
	if runWaitTimeout(PriorityScheduled) >= runWaitTimeout(PriorityChannel) {
		t.Fatal("a channel message must be allowed to wait longer than a scheduled job")
	}
	if runWaitTimeout(PriorityChannel) >= runWaitTimeout(PriorityOperator) {
		t.Fatal("the operator must be allowed to wait longest")
	}
}
