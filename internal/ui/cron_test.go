package ui

import (
	"strings"
	"testing"
)

// TestCronCommandManagesJobs drives the user-visible lifecycle: add, list,
// disable, remove.
func TestCronCommandManagesJobs(t *testing.T) {
	model := chatModel(t)

	if _, out := runSlash(t, model, "/cron add every 30m :: write a report"); !strings.Contains(out, "Added job") {
		t.Fatalf("add output = %q", out)
	}
	jobs, err := model.app.CronJobs()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("CronJobs = %v, %v", jobs, err)
	}
	id := jobs[0].ID

	if _, out := runSlash(t, model, "/cron list"); !strings.Contains(out, "scheduled job") {
		t.Fatalf("list output = %q", out)
	}
	if _, out := runSlash(t, model, "/cron off "+id); !strings.Contains(out, "now off") {
		t.Fatalf("off output = %q", out)
	}
	if _, out := runSlash(t, model, "/cron remove "+id); !strings.Contains(out, "Removed job") {
		t.Fatalf("remove output = %q", out)
	}
	if jobs, _ := model.app.CronJobs(); len(jobs) != 0 {
		t.Errorf("the job should be gone, got %v", jobs)
	}
}

func TestCronCommandRejectsABadSchedule(t *testing.T) {
	model := chatModel(t)
	if _, out := runSlash(t, model, "/cron add nonsense :: do it"); !strings.Contains(out, "five fields") {
		t.Fatalf("a bad schedule should be reported, got %q", out)
	}
}

func TestAuditCommandRuns(t *testing.T) {
	model := chatModel(t)
	if _, out := runSlash(t, model, "/audit"); !strings.Contains(out, "audit") && !strings.Contains(out, "Audit") {
		t.Fatalf("audit output = %q", out)
	}
}

// TestHeartbeatCommandToggles covers the periodic self-check switch.
func TestHeartbeatCommandToggles(t *testing.T) {
	model := chatModel(t)
	if _, out := runSlash(t, model, "/heartbeat"); !strings.Contains(out, "off") {
		t.Fatalf("status = %q", out)
	}
	if _, out := runSlash(t, model, "/heartbeat on"); !strings.Contains(out, "on") {
		t.Fatalf("on = %q", out)
	}
	if !model.app.Config().Heartbeat.Enabled {
		t.Errorf("heartbeat should be enabled")
	}
	if _, out := runSlash(t, model, "/heartbeat 45m"); !strings.Contains(out, "45m") {
		t.Fatalf("interval = %q", out)
	}
	if got := model.app.Config().Heartbeat.Interval; got != "45m" {
		t.Errorf("interval = %q, want 45m", got)
	}
}
