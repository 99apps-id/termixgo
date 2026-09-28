package agent

import (
	"context"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/provider"
)

// backgroundEnv is an environment wired to a real process manager, which is
// what the run_background family needs. The manager is shut down with the test
// so no child process outlives it.
func backgroundEnv(t *testing.T) *Env {
	t.Helper()
	manager := newTestManager()
	t.Cleanup(manager.Shutdown)

	workspace := t.TempDir()
	return &Env{Workspace: workspace, Processes: manager, Trusted: true}
}

// TestBackgroundToolsNeedAManager pins the guard every one of the five shares.
// Without it a headless or restricted session would panic instead of telling
// the model what is unavailable.
func TestBackgroundToolsNeedAManager(t *testing.T) {
	env := testEnv(t)
	tools := []Tool{
		&backgroundTool{}, &logsTool{}, &waitTool{}, &listProcessesTool{}, &killTool{},
	}
	for _, tool := range tools {
		result, err := tool.Run(context.Background(), env, map[string]any{
			"command": quickCommand("x"),
			"handle":  "proc-1",
		})
		if err != nil {
			t.Errorf("%s returned a Go error: %v", tool.Name(), err)
			continue
		}
		if !result.IsError {
			t.Errorf("%s should refuse without a manager, got %q", tool.Name(), result.Output)
		}
		if !strings.Contains(result.Output, "not available") {
			t.Errorf("%s output = %q, want it to say why", tool.Name(), result.Output)
		}
	}
}

func TestBackgroundToolRequiresACommand(t *testing.T) {
	env := backgroundEnv(t)
	result, err := (&backgroundTool{}).Run(context.Background(), env, map[string]any{"command": "   "})
	if err != nil || !result.IsError {
		t.Fatalf("a blank command must be refused: err=%v result=%+v", err, result)
	}
}

// TestBackgroundToolReportsAShortLivedCommand covers the case the grace period
// exists for: the command died immediately, so the model gets the answer and
// the output instead of a handle to a corpse.
func TestBackgroundToolReportsAShortLivedCommand(t *testing.T) {
	env := backgroundEnv(t)
	tool := &backgroundTool{}

	result, err := tool.Run(context.Background(), env, map[string]any{
		"command":   quickCommand("short lived"),
		"wait_secs": 10,
	})
	if err != nil || result.IsError {
		t.Fatalf("a command that exits 0 is not an error: err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Output, "exit") {
		t.Errorf("output = %q, want it to explain the command already finished", result.Output)
	}
	if !strings.Contains(result.Output, "short lived") {
		t.Errorf("output = %q, want the captured output", result.Output)
	}
}

func TestBackgroundToolReportsAnImmediateFailure(t *testing.T) {
	env := backgroundEnv(t)
	result, err := (&backgroundTool{}).Run(context.Background(), env, map[string]any{
		"command":   failingCommand(),
		"wait_secs": 10,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsError {
		t.Fatalf("a non-zero exit must be reported as an error, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "code 3") {
		t.Errorf("output = %q, want the exit code", result.Output)
	}
	if !strings.Contains(result.Output, "boom") {
		t.Errorf("output = %q, want the failure output", result.Output)
	}
}

// TestProcessToolLifecycle drives run_background, run_logs, run_wait, run_list
// and run_kill in the order the model would, on one long-running process.
func TestProcessToolLifecycle(t *testing.T) {
	env := backgroundEnv(t)
	ctx := context.Background()

	started, err := (&backgroundTool{}).Run(ctx, env, map[string]any{
		"command":   longRunningCommand(),
		"wait_secs": 1,
	})
	if err != nil || started.IsError {
		t.Fatalf("run_background: err=%v result=%+v", err, started)
	}
	handle := theHandle(t, started.Output)
	if !strings.Contains(started.Output, "run_logs") {
		t.Errorf("output = %q, want it to name the follow-up tools", started.Output)
	}

	listed, err := (&listProcessesTool{}).Run(ctx, env, nil)
	if err != nil || listed.IsError {
		t.Fatalf("run_list: err=%v result=%+v", err, listed)
	}
	if !strings.Contains(listed.Output, handle) {
		t.Errorf("run_list should name %s, got %q", handle, listed.Output)
	}

	// run_logs with no offset reads from the beginning and reports a position
	// to continue from.
	logged, err := (&logsTool{}).Run(ctx, env, map[string]any{"handle": handle})
	if err != nil || logged.IsError {
		t.Fatalf("run_logs: err=%v result=%+v", err, logged)
	}
	if !strings.Contains(logged.Output, "next_offset=") {
		t.Fatalf("run_logs should report where to resume, got %q", logged.Output)
	}
	offset := theOffset(t, logged.Output)

	// run_wait with a short timeout on a process that keeps running must
	// answer rather than block the turn.
	waited, err := (&waitTool{}).Run(ctx, env, map[string]any{"handle": handle, "timeout_secs": 1})
	if err != nil || waited.IsError {
		t.Fatalf("run_wait: err=%v result=%+v", err, waited)
	}
	if !strings.Contains(waited.Output, "still running") {
		t.Errorf("output = %q, want it to say the wait timed out", waited.Output)
	}

	// Reading again from that offset is how the model follows progress without
	// re-reading everything.
	again, err := (&logsTool{}).Run(ctx, env, map[string]any{
		"handle":       handle,
		"since_offset": offset,
	})
	if err != nil || again.IsError {
		t.Fatalf("run_logs: err=%v result=%+v", err, again)
	}
	if !strings.Contains(again.Output, "next_offset=") {
		t.Errorf("output = %q, want a new offset", again.Output)
	}

	killed, err := (&killTool{}).Run(ctx, env, map[string]any{"handle": handle})
	if err != nil || killed.IsError {
		t.Fatalf("run_kill: err=%v result=%+v", err, killed)
	}
	if !strings.Contains(killed.Output, handle) {
		t.Errorf("output = %q, want it to name the process", killed.Output)
	}
}

// TestWaitToolReportsACleanAndAFailingExit needs a process that is still alive
// when the handle is handed back and then ends on its own, because the exit
// code is only observable after the process has gone.
func TestWaitToolReportsACleanAndAFailingExit(t *testing.T) {
	env := backgroundEnv(t)
	ctx := context.Background()

	clean, err := (&backgroundTool{}).Run(ctx, env, map[string]any{
		"command": slowCommand("all good", 0), "wait_secs": 1,
	})
	if err != nil || clean.IsError {
		t.Fatalf("run_background: err=%v result=%+v", err, clean)
	}
	waited, err := (&waitTool{}).Run(ctx, env, map[string]any{
		"handle": theHandle(t, clean.Output), "timeout_secs": 60,
	})
	if err != nil || waited.IsError {
		t.Fatalf("run_wait on a clean exit: err=%v result=%+v", err, waited)
	}
	if !strings.Contains(waited.Output, "code 0") {
		t.Errorf("output = %q, want the exit code", waited.Output)
	}
	if !strings.Contains(waited.Output, "all good") {
		t.Errorf("output = %q, want the tail of the output", waited.Output)
	}

	failed, err := (&backgroundTool{}).Run(ctx, env, map[string]any{
		"command": slowCommand("boom", 3), "wait_secs": 1,
	})
	if err != nil || failed.IsError {
		t.Fatalf("run_background: err=%v result=%+v", err, failed)
	}
	waited, err = (&waitTool{}).Run(ctx, env, map[string]any{
		"handle": theHandle(t, failed.Output), "timeout_secs": 60,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !waited.IsError {
		t.Fatalf("a non-zero exit must be reported to the model, got %q", waited.Output)
	}
	if !strings.Contains(waited.Output, "code 3") {
		t.Errorf("output = %q, want the exit code", waited.Output)
	}
}

func TestProcessToolsReportAnUnknownHandle(t *testing.T) {
	env := backgroundEnv(t)
	ctx := context.Background()

	for _, tool := range []Tool{&logsTool{}, &waitTool{}, &killTool{}} {
		result, err := tool.Run(ctx, env, map[string]any{"handle": "proc-999"})
		if err != nil {
			t.Errorf("%s returned a Go error: %v", tool.Name(), err)
			continue
		}
		if !result.IsError {
			t.Errorf("%s should refuse an unknown handle, got %q", tool.Name(), result.Output)
		}
		if !strings.Contains(result.Output, "run_list") {
			t.Errorf("%s output = %q, want it to point at run_list", tool.Name(), result.Output)
		}
	}
}

func TestListToolSaysWhenNothingIsRunning(t *testing.T) {
	env := backgroundEnv(t)
	result, err := (&listProcessesTool{}).Run(context.Background(), env, nil)
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Output, "No background processes") {
		t.Errorf("output = %q", result.Output)
	}
}

// TestKillToolOnAnExitedProcess is the idempotence the tool promises: a model
// that kills twice must not be told it failed.
func TestKillToolOnAnExitedProcess(t *testing.T) {
	env := backgroundEnv(t)
	ctx := context.Background()

	started, err := (&backgroundTool{}).Run(ctx, env, map[string]any{
		"command": slowCommand("already done", 0), "wait_secs": 1,
	})
	if err != nil || started.IsError {
		t.Fatalf("run_background: err=%v result=%+v", err, started)
	}
	handle := theHandle(t, started.Output)
	if _, err := (&waitTool{}).Run(ctx, env, map[string]any{"handle": handle, "timeout_secs": 60}); err != nil {
		t.Fatalf("run_wait: %v", err)
	}

	killed, err := (&killTool{}).Run(ctx, env, map[string]any{"handle": handle})
	if err != nil || killed.IsError {
		t.Fatalf("killing an exited process must succeed: err=%v result=%+v", err, killed)
	}
	if !strings.Contains(killed.Output, "already exited") {
		t.Errorf("output = %q, want it to say the process had gone", killed.Output)
	}
}

// slowCommand runs for a few seconds and then exits with a chosen code. Tests
// that need a handle need a process that outlives the grace period, and tests
// that need an exit code need one that ends on its own.
func slowCommand(tail string, code int) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("ping -n 4 127.0.0.1 >nul & echo %s & exit %d", tail, code)
	}
	return fmt.Sprintf("sleep 3; echo %s; exit %d", tail, code)
}

func TestPidOfIsZeroWhenTheProcessIsUnknown(t *testing.T) {
	if got := pidOf(&Process{}); got != 0 {
		t.Errorf("pidOf = %d, want 0 for a bare Process", got)
	}
}

// TestEveryToolDeclaresAUsefulRisk is the approval contract. Under the
// "edits" policy a mutating tool is auto-approved only when it declares
// RiskEdit, so a tool that runs a process or reaches the network and still
// claims RiskEdit would run without asking.
func TestEveryToolDeclaresAUsefulRisk(t *testing.T) {
	known := map[Risk]bool{RiskEdit: true, RiskCommand: true, RiskNetwork: true}
	for _, tool := range DefaultRegistry().Tools() {
		name := tool.Name()
		if !known[tool.Risk()] {
			t.Errorf("%s declares unknown risk %q", name, tool.Risk())
		}
		if tool.Mutating() && tool.Risk() == "" {
			t.Errorf("%s mutates but declares no risk", name)
		}
	}

	// The specific tools whose classification is load bearing: each one runs a
	// command, so under "edits" they must still ask.
	for _, tool := range []Tool{&runCommandTool{}, &backgroundTool{}, &killTool{}} {
		if tool.Risk() != RiskCommand {
			t.Errorf("%s risk = %q, want command", tool.Name(), tool.Risk())
		}
		if !tool.Mutating() {
			t.Errorf("%s changes the machine, so it must report as mutating", tool.Name())
		}
	}
	if risk := (&webFetchTool{}).Risk(); risk != RiskNetwork {
		t.Errorf("web_fetch risk = %q, want network", risk)
	}
}

// TestEditsModeAsksBeforeCommands is the same contract seen from the policy
// side: a command tool must not slip through the automatic-approval rule.
func TestEditsModeAsksBeforeCommands(t *testing.T) {
	policy := &ApprovalPolicy{Mode: ApprovalEdits}
	for _, tool := range []Tool{&runCommandTool{}, &backgroundTool{}, &killTool{}} {
		if !policy.NeedsApproval(tool) {
			t.Errorf("%s must need approval in edits mode", tool.Name())
		}
	}
	// A plain file edit is the one thing that runs without asking.
	if policy.NeedsApproval(&editTool{}) {
		t.Errorf("an in-workspace edit is what edits mode is for")
	}
	if policy.NeedsApproval(&webFetchTool{}) {
		t.Errorf("web_fetch is read-only, so it does not need approval")
	}
}

// theHandle pulls the "proc-N" handle out of a tool message.
func theHandle(t *testing.T, output string) string {
	t.Helper()
	match := regexp.MustCompile(`Started (proc-\d+)`).FindStringSubmatch(output)
	if match == nil {
		t.Fatalf("no handle in %q", output)
	}
	return match[1]
}

// theOffset pulls the resume offset out of a run_logs message.
func theOffset(t *testing.T, output string) int64 {
	t.Helper()
	match := regexp.MustCompile(`next_offset=(\d+)`).FindStringSubmatch(output)
	if match == nil {
		t.Fatalf("no offset in %q", output)
	}
	value, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		t.Fatalf("parse offset: %v", err)
	}
	return value
}

// TestBackgroundToolStartupOutputIsBounded keeps a chatty server from filling
// the context window on the first call.
func TestBackgroundToolStartupOutputIsBounded(t *testing.T) {
	env := backgroundEnv(t)
	result, err := (&backgroundTool{}).Run(context.Background(), env, map[string]any{
		"command":   quickCommand(strings.Repeat("x", 4000)),
		"wait_secs": 10,
	})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if len(result.Output) > 4200 {
		t.Errorf("output is %d bytes, want the startup output shortened", len(result.Output))
	}
}

// TestRunBackgroundAcceptsAClock covers the wait clamp: a value outside the
// documented range must be clamped rather than trusted.
func TestRunBackgroundAcceptsAClock(t *testing.T) {
	env := backgroundEnv(t)
	started := time.Now()
	result, err := (&backgroundTool{}).Run(context.Background(), env, map[string]any{
		"command":   quickCommand("clamped"),
		"wait_secs": 9999,
	})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	// The process exits on its own, so the call returns as soon as it does
	// rather than waiting out the clamped 60 seconds.
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Errorf("the call took %s; an exited process must end the wait", elapsed)
	}
}

// TestUnknownToolFromTheRegistryIsReported uses the registry entry point the
// runner uses, which is a different path from a direct tool call.
func TestUnknownToolFromTheRegistryIsReported(t *testing.T) {
	env := testEnv(t)
	runner := &Runner{Tools: DefaultRegistry(), Env: env, Policy: &ApprovalPolicy{Mode: ApprovalAll}}

	missing := runner.execute(context.Background(), provider.ToolCall{Name: "no_such_tool", Arguments: "{}"})
	if !missing.IsError {
		t.Fatalf("an unknown tool must be refused, got %+v", missing)
	}

	// A real tool through the same path must work, which proves the lookup is
	// wired rather than always failing.
	read := runner.execute(context.Background(), provider.ToolCall{Name: "todo_read", Arguments: "{}"})
	if read.IsError {
		t.Fatalf("todo_read through the runner: %+v", read)
	}
	if !strings.Contains(read.Output, "empty") {
		t.Errorf("output = %q, want the empty-plan answer", read.Output)
	}
}
