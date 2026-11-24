package cron

import (
	"context"
	"testing"
	"time"
)

type panicRunner struct{}

func (panicRunner) RunScheduled(context.Context, Job) (string, error) { panic("boom") }

// One job that panics must not take the scheduler, and the process, with it.
func TestAPanickingJobDoesNotCrashTheScheduler(t *testing.T) {
	var logged []string
	scheduler := &Scheduler{Runner: panicRunner{}, Now: time.Now, Log: func(line string) { logged = append(logged, line) }}
	scheduler.wg.Add(1)
	scheduler.claim("job-1")
	scheduler.run(context.Background(), Job{ID: "job-1", Prompt: "x"})
	if len(logged) == 0 {
		t.Fatalf("the panic was swallowed without a trace")
	}
	if !scheduler.claim("job-1") {
		t.Errorf("a panicked job must release its slot so it can run again")
	}
}
