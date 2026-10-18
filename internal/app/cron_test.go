package app

import (
	"context"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/provider"
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

// TestScheduledRunDoesNotFoldUsageIntoTheLiveSession pins the isolated emitter:
// a background turn accrues spend on its throwaway session, never on the
// operator's live app total or session.
func TestScheduledRunDoesNotFoldUsageIntoTheLiveSession(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()
	defer application.Shutdown()

	live := application.Session()
	before := application.Usage()

	env := application.isolatedEnv()
	env.Emit(agent.Event{
		Kind:      agent.EventUsage,
		Usage:     provider.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
		CostUSD:   0.01,
		CostKnown: true,
	})

	if after := application.Usage(); after != before {
		t.Errorf("an isolated run's usage leaked into the live app total: %+v -> %+v", before, after)
	}
	if live.Usage() != before {
		t.Errorf("an isolated run's usage leaked into the live session: %+v -> %+v", before, live.Usage())
	}
}

// TestScheduledRunGetsAThrowawayTodoStore keeps a background turn from
// rewriting the operator's plan: its todo_write lands on its own store.
func TestScheduledRunGetsAThrowawayTodoStore(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()
	defer application.Shutdown()

	application.todos.Set([]agent.Todo{{Title: "operator plan", Status: "in_progress"}})
	env := application.isolatedEnv()

	if env.Todos == application.todos {
		t.Fatalf("an isolated run must not share the operator's todo store")
	}
	if items := env.Todos.Items(); len(items) != 0 {
		t.Errorf("an isolated run should start with an empty plan, got %v", items)
	}
	if items := application.todos.Items(); len(items) != 1 || items[0].Title != "operator plan" {
		t.Errorf("the operator's plan was disturbed: %v", items)
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
