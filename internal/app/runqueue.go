package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// RunPriority orders callers that want the single run slot. A higher value is
// served first. The operator outranks a chat message, a chat message outranks a
// scheduled job, and a scheduled job outranks the heartbeat, so a background
// self-check can never make the person at the terminal wait.
type RunPriority int

const (
	// PriorityHeartbeat is the periodic self-check. It never waits: if the slot
	// is busy the beat is skipped, because the next one is close.
	PriorityHeartbeat RunPriority = 1
	// PriorityScheduled is a cron job. It waits briefly, then reports busy so
	// the scheduler requeues the occurrence instead of losing it.
	PriorityScheduled RunPriority = 2
	// PriorityChannel is a message from WhatsApp or Telegram. It waits longer
	// than a schedule but not forever, so a chat cannot pile up behind a long
	// operator turn.
	PriorityChannel RunPriority = 3
	// PriorityOperator is the terminal and `termixgo run`. The zero value of
	// runOptions means this, which keeps every existing caller correct.
	PriorityOperator RunPriority = 4
)

// runWaitTimeout bounds how long a caller may wait for the slot. Zero means
// "do not wait at all", which is the heartbeat.
func runWaitTimeout(priority RunPriority) time.Duration {
	switch priority {
	case PriorityHeartbeat:
		return 0
	case PriorityScheduled:
		return 30 * time.Second
	case PriorityChannel:
		return 3 * time.Minute
	default:
		return 10 * time.Minute
	}
}

// runQueue grants the single run slot with priority. A condition variable would
// be simpler, but it cannot observe a cancelled context, and a waiter whose
// caller gave up must leave the queue instead of being granted a slot nobody
// will release. Each waiter therefore registers a small goroutine that wakes the
// condition when its context ends.
type runQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	holder  RunPriority
	busy    bool
	waiting []*runWaiter
	closed  bool
}

type runWaiter struct {
	priority RunPriority
}

func newRunQueue() *runQueue {
	queue := &runQueue{}
	queue.cond = sync.NewCond(&queue.mu)
	return queue
}

// acquire waits for the slot and takes it.
func (q *runQueue) acquire(ctx context.Context, priority RunPriority) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return ErrBusy
	}
	if !q.busy && len(q.waiting) == 0 {
		q.busy = true
		q.holder = priority
		return nil
	}

	entry := &runWaiter{priority: priority}
	q.waiting = append(q.waiting, entry)
	defer q.removeLocked(entry)

	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// The lock is what makes this a real wakeup: the waiter cannot be
			// between its context check and Wait, because it holds the same
			// mutex until Wait releases it.
			q.mu.Lock()
			q.cond.Broadcast()
			q.mu.Unlock()
		case <-stopWatch:
		}
	}()
	// Registered after the unlock defer below, so it runs before it and the
	// watcher is never left blocked on a mutex this method holds.
	defer close(stopWatch)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if q.closed {
			return ErrBusy
		}
		if !q.busy && q.preferredLocked(entry) {
			q.removeLocked(entry)
			q.busy = true
			q.holder = priority
			return nil
		}
		q.cond.Wait()
	}
}

// tryAcquire takes the slot only when nothing is running or waiting.
func (q *runQueue) tryAcquire(priority RunPriority) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.busy || len(q.waiting) > 0 {
		return false
	}
	q.busy = true
	q.holder = priority
	return true
}

// release hands the slot to whoever is waiting.
func (q *runQueue) release() {
	q.mu.Lock()
	q.busy = false
	q.holder = 0
	q.cond.Broadcast()
	q.mu.Unlock()
}

// close refuses every future caller and wakes the current waiters.
func (q *runQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// Holder reports the priority of the run in flight, and whether one is running.
func (q *runQueue) Holder() (RunPriority, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.holder, q.busy
}

// Waiting reports how many callers are queued.
func (q *runQueue) Waiting() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.waiting)
}

// preferredLocked reports whether no other waiter outranks this one.
func (q *runQueue) preferredLocked(entry *runWaiter) bool {
	for _, other := range q.waiting {
		if other == entry {
			continue
		}
		if other.priority > entry.priority {
			return false
		}
	}
	return true
}

// acquireTurn waits for the run slot at the caller's priority and returns the
// release function. A heartbeat never waits; every other surface waits for its
// own bound and is then told the agent is busy, which keeps a queue from
// growing without limit.
func (a *App) acquireTurn(ctx context.Context, priority RunPriority) (func(), error) {
	if a.runQueue == nil {
		// A hand-built App in a test has no queue; runMu still serialises it.
		return func() {}, nil
	}
	wait := runWaitTimeout(priority)
	if wait == 0 {
		if !a.runQueue.tryAcquire(priority) {
			return nil, ErrBusy
		}
		return a.runQueue.release, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	err := a.runQueue.acquire(waitCtx, priority)
	cancel()
	if err == nil {
		return a.runQueue.release, nil
	}
	// A caller that cancelled the wait itself (Esc, a stopped channel turn)
	// keeps its own reason; a timeout is the ordinary busy answer.
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, ErrBusy
}

// runQueueStatus describes the slot for a status line.
func (a *App) runQueueStatus() string {
	if a.runQueue == nil {
		return "idle"
	}
	priority, busy := a.runQueue.Holder()
	if !busy {
		if waiting := a.runQueue.Waiting(); waiting > 0 {
			return fmt.Sprintf("idle, %d waiting", waiting)
		}
		return "idle"
	}
	return fmt.Sprintf("running at priority %d, %d waiting", priority, a.runQueue.Waiting())
}

// removeLocked drops one waiter from the queue. The slice holds at most a
// handful of entries, so the linear scan is cheaper than an index map.
func (q *runQueue) removeLocked(entry *runWaiter) {
	for index, other := range q.waiting {
		if other == entry {
			q.waiting = append(q.waiting[:index], q.waiting[index+1:]...)
			return
		}
	}
}
