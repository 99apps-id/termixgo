package app

import (
	"context"
	"testing"
)

func TestRunScheduledKeepsTheLiveSessionClean(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()
	defer application.Shutdown()

	live := application.Session()
	before := len(live.Messages())

	output, err := application.RunScheduled(context.Background(), "say ok", nil)
	if err != nil {
		t.Fatalf("RunScheduled: %v", err)
	}
	if output == "" {
		t.Errorf("a scheduled run should return the answer")
	}
	if len(live.Messages()) != before {
		t.Errorf("a scheduled run must not touch the operator session: %d -> %d", before, len(live.Messages()))
	}
}

func TestCronJobLifecycle(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()
	defer application.Shutdown()

	job, err := application.CronAdd("report", "every 30m", "write the report")
	if err != nil {
		t.Fatalf("CronAdd: %v", err)
	}
	jobs, err := application.CronJobs()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("CronJobs = %v, %v", jobs, err)
	}
	if _, ok, err := application.CronSetEnabled(job.ID, false); err != nil || !ok {
		t.Fatalf("CronSetEnabled: ok=%v err=%v", ok, err)
	}
	if removed, err := application.CronRemove(job.ID); err != nil || !removed {
		t.Fatalf("CronRemove: removed=%v err=%v", removed, err)
	}
	if jobs, _ := application.CronJobs(); len(jobs) != 0 {
		t.Errorf("the job should be gone, got %v", jobs)
	}
}

func TestDeliverWithoutAPairingReportsIt(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()
	defer application.Shutdown()

	if err := application.Deliver(context.Background(), "hello"); err == nil {
		t.Errorf("delivering with no paired chat should fail")
	}
}

func TestHeartbeatConfig(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()
	defer application.Shutdown()

	if got := application.HeartbeatStatus(); got != "off" {
		t.Errorf("heartbeat should default off, got %q", got)
	}
	if err := application.SetHeartbeat(true, "45m"); err != nil {
		t.Fatalf("SetHeartbeat: %v", err)
	}
	if got := application.HeartbeatStatus(); got != "every 45m" {
		t.Errorf("HeartbeatStatus = %q, want every 45m", got)
	}
}
