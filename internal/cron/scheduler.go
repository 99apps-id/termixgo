package cron

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Tokens a scheduled turn can answer with to stay silent.
const (
	// SilentToken suppresses delivery for a cron job that decided it has
	// nothing to report.
	SilentToken = "[SILENT]"
	// HeartbeatToken suppresses delivery for the periodic heartbeat.
	HeartbeatToken = "HEARTBEAT_OK"
)

// DefaultInterval is how often the scheduler looks for due jobs.
const DefaultInterval = 30 * time.Second

// Runner is the agent side of a scheduled job. It runs one isolated turn and
// returns the final answer text.
type Runner interface {
	RunScheduled(ctx context.Context, prompt string, progress func(string)) (string, error)
}

// Deliverer sends a job's result to the operator, normally the paired chat.
type Deliverer interface {
	Deliver(ctx context.Context, text string) error
}

// Heartbeat is the periodic self-check. When it has nothing to say the turn
// answers HeartbeatToken and the result is dropped.
type Heartbeat struct {
	Interval time.Duration
	Prompt   string
}

// Scheduler owns the tick loop. One job runs at a time per id; the app's own
// single-run gate is the real backstop, so a slow job never overlaps a turn.
type Scheduler struct {
	Store    *Store
	Runner   Runner
	Deliver  Deliverer
	Now      func() time.Time
	Interval time.Duration
	Log      func(string)

	mu       sync.Mutex
	inFlight map[string]bool
	wg       sync.WaitGroup

	heartbeat *Heartbeat
	beatNext  time.Time
}

// Wait blocks until every job started so far has finished. It is used on
// shutdown and by tests, so a delivered result is observed deterministically.
func (s *Scheduler) Wait() { s.wg.Wait() }

// SetHeartbeat installs the periodic heartbeat. The first beat is one interval
// away, so starting the assistant does not immediately run a turn.
func (s *Scheduler) SetHeartbeat(heartbeat *Heartbeat) {
	s.heartbeat = heartbeat
	s.beatNext = time.Time{}
}

// Run ticks until the context is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	s.tick(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	// Re-read the jobs so an edit made by another process (the CLI while
	// `serve` runs) is picked up on the next tick.
	if s.Store != nil {
		_ = s.Store.Reload()
	}
	now := s.now()
	for _, job := range s.Store.Due(now) {
		if !s.claim(job.ID) {
			continue
		}
		advanced, ok, err := s.Store.MarkStarted(job.ID, now)
		if err != nil || !ok {
			s.release(job.ID)
			if err != nil {
				s.logf("could not start job %s: %v", jobLabel(job), err)
			}
			continue
		}
		s.wg.Add(1)
		go s.run(ctx, advanced.ID, advanced.Prompt)
	}
	s.tickHeartbeat(ctx, now)
}

func (s *Scheduler) tickHeartbeat(ctx context.Context, now time.Time) {
	heartbeat := s.heartbeat
	if heartbeat == nil || heartbeat.Interval <= 0 {
		return
	}
	if s.beatNext.IsZero() {
		s.beatNext = now.Add(heartbeat.Interval)
		return
	}
	if s.beatNext.After(now) {
		return
	}
	s.beatNext = now.Add(heartbeat.Interval)
	if !s.claim("heartbeat") {
		return
	}
	s.wg.Add(1)
	go s.run(ctx, "heartbeat", heartbeat.Prompt)
}

// run executes one job and delivers its result unless it asked to stay silent.
func (s *Scheduler) run(ctx context.Context, id, prompt string) {
	defer s.wg.Done()
	defer s.release(id)
	output, err := s.Runner.RunScheduled(ctx, prompt, nil)
	if err != nil {
		_ = s.Store.MarkError(id, err)
		s.logf("job %s failed: %v", id, err)
		return
	}
	_ = s.Store.MarkError(id, nil)
	if suppressed(output) || s.Deliver == nil {
		return
	}
	if err := s.Deliver.Deliver(ctx, strings.TrimSpace(output)); err != nil {
		s.logf("job %s could not be delivered: %v", id, err)
	}
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) claim(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight == nil {
		s.inFlight = map[string]bool{}
	}
	if s.inFlight[id] {
		return false
	}
	s.inFlight[id] = true
	return true
}

func (s *Scheduler) release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inFlight, id)
}

func (s *Scheduler) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(fmt.Sprintf(format, args...))
	}
}

// suppressed reports whether a result is a deliberate silence rather than an
// answer worth sending.
func suppressed(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	return strings.EqualFold(trimmed, SilentToken) || strings.EqualFold(trimmed, HeartbeatToken)
}

func jobLabel(job Job) string {
	if strings.TrimSpace(job.Name) != "" {
		return job.Name
	}
	return job.ID
}
