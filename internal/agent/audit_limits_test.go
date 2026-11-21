package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A command result names the directory in words: "in ." read as a missing
// duration.
func TestCommandResultNamesTheWorkspaceRoot(t *testing.T) {
	if got := dirLabel("."); got != "the workspace root" {
		t.Fatalf("dirLabel(.) = %q", got)
	}
	if got := dirLabel("sub/dir"); got != "sub/dir" {
		t.Fatalf("dirLabel(sub/dir) = %q", got)
	}
	env := testEnv(t)
	env.Trusted = true
	result, _ := (&runCommandTool{}).Run(context.Background(), env, map[string]any{"command": quickCommand("hi")})
	if result.IsError || !strings.Contains(result.Output, "Command succeeded in the workspace root") {
		t.Fatalf("result = %+v", result)
	}
}

func TestMonitorRefusesInAChatTurnAndCapsWatchers(t *testing.T) {
	manager := NewProcessManager()
	manager.SetEmitter(func(Event) {})
	env := testEnv(t)
	env.Processes = manager
	env.NoWake = true
	result, _ := (&monitorTool{}).Run(context.Background(), env, map[string]any{"handle": "proc-1", "pattern": "x"})
	if !result.IsError || !strings.Contains(result.Output, "not available in this conversation") {
		t.Fatalf("a chat turn must not start a monitor: %+v", result)
	}
	for i := 0; i < maxMonitors; i++ {
		if !manager.acquireMonitor() {
			t.Fatalf("slot %d should be free", i)
		}
	}
	if manager.acquireMonitor() {
		t.Fatalf("the watcher cap must hold")
	}
	manager.releaseMonitor()
	if !manager.acquireMonitor() {
		t.Fatalf("a released slot is reusable")
	}
}

func TestBackgroundSubagentsAreCapped(t *testing.T) {
	pool := NewSubagentPool()
	for i := 0; i < maxBackgroundSubagents; i++ {
		entry := pool.create("general", "worker")
		if err := pool.begin(entry, true, func() {}); err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	extra := pool.create("general", "one too many")
	if err := pool.begin(extra, true, func() {}); err == nil || !strings.Contains(err.Error(), "background subagents") {
		t.Fatalf("the cap must refuse another background run: %v", err)
	}
	// A foreground run is not a detached worker and is not counted.
	if err := pool.begin(extra, false, nil); err != nil {
		t.Fatalf("a foreground run must still start: %v", err)
	}
	_ = time.Second
}
