package cron

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu      sync.Mutex
	prompts []string
	answer  string
	err     error
}

func (f *fakeRunner) RunScheduled(_ context.Context, prompt string, _ func(string)) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, prompt)
	return f.answer, f.err
}

func (f *fakeRunner) setAnswer(answer string) {
	f.mu.Lock()
	f.answer = answer
	f.mu.Unlock()
}

func (f *fakeRunner) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts)
}

type fakeDeliverer struct {
	mu    sync.Mutex
	texts []string
}

func (d *fakeDeliverer) Deliver(_ context.Context, text string) error {
	d.mu.Lock()
	d.texts = append(d.texts, text)
	d.mu.Unlock()
	return nil
}

func (d *fakeDeliverer) delivered() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.texts))
	copy(out, d.texts)
	return out
}

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "cron", "jobs.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

func TestSchedulerDeliversADueJob(t *testing.T) {
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	store := newStore(t)
	if _, err := store.Add("daily", "write the report", Schedule{Kind: Every, Every: time.Hour}, now); err != nil {
		t.Fatalf("Add: %v", err)
	}

	runner := &fakeRunner{answer: "the report"}
	deliverer := &fakeDeliverer{}
	clock := now
	scheduler := &Scheduler{
		Store:   store,
		Runner:  runner,
		Deliver: deliverer,
		Now:     func() time.Time { return clock },
	}

	// Not due yet: the first run is one hour away.
	scheduler.tick(context.Background())
	scheduler.Wait()
	if got := deliverer.delivered(); len(got) != 0 {
		t.Fatalf("a job before its time should not run, delivered %v", got)
	}

	// Advance past the due time and tick again.
	clock = now.Add(2 * time.Hour)
	scheduler.tick(context.Background())
	scheduler.Wait()

	if got := deliverer.delivered(); len(got) != 1 || got[0] != "the report" {
		t.Fatalf("delivered = %v, want the report once", got)
	}
	jobs := store.List()
	if !jobs[0].NextRun.After(clock) {
		t.Errorf("next run should advance past now, got %v", jobs[0].NextRun)
	}
}

func TestSchedulerSuppressesSilentJobs(t *testing.T) {
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	store := newStore(t)
	if _, err := store.Add("quiet", "check things", Schedule{Kind: Every, Every: time.Minute}, now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	deliverer := &fakeDeliverer{}
	scheduler := &Scheduler{
		Store:   store,
		Runner:  &fakeRunner{answer: "[SILENT]"},
		Deliver: deliverer,
		Now:     func() time.Time { return now.Add(2 * time.Minute) },
	}
	scheduler.tick(context.Background())
	scheduler.Wait()

	if got := deliverer.delivered(); len(got) != 0 {
		t.Errorf("a silent job must not be delivered, got %v", got)
	}
}

func TestSchedulerDisablesAOneShot(t *testing.T) {
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	store := newStore(t)
	if _, err := store.Add("once", "remind me", Schedule{Kind: At, At: now.Add(time.Hour)}, now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	clock := now.Add(2 * time.Hour)
	scheduler := &Scheduler{
		Store:   store,
		Runner:  &fakeRunner{answer: "reminder"},
		Deliver: &fakeDeliverer{},
		Now:     func() time.Time { return clock },
	}
	scheduler.tick(context.Background())
	scheduler.Wait()

	job := store.List()[0]
	if job.Enabled {
		t.Errorf("a fired one-shot should be disabled")
	}
	if !job.NextRun.IsZero() {
		t.Errorf("a fired one-shot should have no next run, got %v", job.NextRun)
	}
}

func TestHeartbeatSuppressesItsAck(t *testing.T) {
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	store := newStore(t)
	runner := &fakeRunner{answer: "HEARTBEAT_OK"}
	deliverer := &fakeDeliverer{}
	clock := now
	scheduler := &Scheduler{
		Store:   store,
		Runner:  runner,
		Deliver: deliverer,
		Now:     func() time.Time { return clock },
	}
	scheduler.SetHeartbeat(&Heartbeat{Interval: time.Hour, Prompt: "beat"})

	// The first tick arms the heartbeat; it does not run yet.
	scheduler.tick(context.Background())
	scheduler.Wait()
	if runner.calls() != 0 {
		t.Fatalf("the heartbeat should not fire on the first tick")
	}

	clock = now.Add(2 * time.Hour)
	scheduler.tick(context.Background())
	scheduler.Wait()
	if got := deliverer.delivered(); len(got) != 0 {
		t.Fatalf("HEARTBEAT_OK must be suppressed, delivered %v", got)
	}

	// A real update is delivered.
	runner.setAnswer("server is up")
	clock = now.Add(3 * time.Hour)
	scheduler.tick(context.Background())
	scheduler.Wait()
	if got := deliverer.delivered(); len(got) != 1 || got[0] != "server is up" {
		t.Fatalf("delivered = %v, want the update", got)
	}
}
