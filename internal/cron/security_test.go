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

// Two processes share the job file. A change made by the other one while this
// copy was open must survive this copy's next write.
func TestAStaleStoreDoesNotUndoAnotherProcessesChange(t *testing.T) {
	path := t.TempDir() + "/jobs.json"
	serve, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := Parse("every 1h")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	running, err := serve.Add("running", "do it", schedule, now)
	if err != nil {
		t.Fatal(err)
	}
	// The CLI adds a job while serve is busy with the first one.
	added, err := cli.Add("added", "later", schedule, now)
	if err != nil {
		t.Fatal(err)
	}
	// Serve finishes and records the result from its stale copy.
	if err := serve.MarkError(running.ID, nil); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Get(added.ID); !ok {
		t.Fatalf("the job added by the other process was lost: %+v", reopened.List())
	}
}
