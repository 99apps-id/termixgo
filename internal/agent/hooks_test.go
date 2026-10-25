package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

// The suite runs foreground commands under cmd.exe on Windows and /bin/sh
// elsewhere (TestMain in process_test.go), so these helpers speak those two
// dialects rather than the PowerShell that production uses.

// shellSay writes msg to stderr and exits with code.
func shellSay(msg string, code int) string {
	if runtime.GOOS == "windows" {
		return "echo " + msg + " 1>&2 & exit " + itoa(code)
	}
	return "echo '" + msg + "' >&2; exit " + itoa(code)
}

// shellSleep waits long enough to outlast a one second hook timeout.
func shellSleep() string {
	if runtime.GOOS == "windows" {
		return "ping -n 20 127.0.0.1 >nul"
	}
	return "sleep 20"
}

// shellCapturePayload saves stdin to payload.json in the working directory.
func shellCapturePayload() string {
	if runtime.GOOS == "windows" {
		return "more > payload.json"
	}
	return "cat > payload.json"
}

func hookRunner(t *testing.T, hooks ...config.Hook) (*Runner, *Env, *recorder) {
	t.Helper()
	runner, env, rec := newTestRunner(t, &fakeClient{}, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.Config.Hooks = hooks
	return runner, env, rec
}

func writeCall(path string) provider.ToolCall {
	return provider.ToolCall{ID: "c1", Name: "write_file", Arguments: `{"path":"` + path + `","content":"one"}`}
}

func hookNotices(rec *recorder) []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var notices []string
	for _, event := range rec.events {
		if event.Kind == EventNotice {
			notices = append(notices, event.Text)
		}
	}
	return notices
}

func TestPreToolHookBlocksTheCallBeforeItRuns(t *testing.T) {
	runner, env, _ := hookRunner(t, config.Hook{
		Event: config.HookPreTool, Matcher: "^write_file$", Command: shellSay("no writes today", 2), TimeoutSecs: 30,
	})

	result := runner.execute(context.Background(), writeCall("blocked.txt"))

	if !result.IsError {
		t.Fatalf("a blocked call must be an error, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "no writes today") {
		t.Errorf("the hook's reason must reach the model, got %q", result.Output)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace, "blocked.txt")); err == nil {
		t.Error("the tool ran even though a pre_tool hook blocked it")
	}
}

func TestPreToolHookMatcherLeavesOtherToolsAlone(t *testing.T) {
	runner, env, _ := hookRunner(t, config.Hook{
		Event: config.HookPreTool, Matcher: "^run_command$", Command: shellSay("no commands", 2), TimeoutSecs: 30,
	})

	result := runner.execute(context.Background(), writeCall("fine.txt"))

	if result.IsError {
		t.Fatalf("a hook for another tool must not block this one: %q", result.Output)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace, "fine.txt")); err != nil {
		t.Errorf("the write should have happened: %v", err)
	}
}

func TestBrokenPreToolHookWarnsAndDoesNotBlock(t *testing.T) {
	runner, env, rec := hookRunner(t, config.Hook{
		Event: config.HookPreTool, Command: shellSay("oops", 1), TimeoutSecs: 30,
	})

	result := runner.execute(context.Background(), writeCall("still.txt"))

	if result.IsError {
		t.Fatalf("exit status 1 is a broken hook, not a policy block: %q", result.Output)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace, "still.txt")); err != nil {
		t.Errorf("the write should have happened: %v", err)
	}
	notices := strings.Join(hookNotices(rec), "\n")
	if !strings.Contains(notices, "not enforced") || !strings.Contains(notices, "oops") {
		t.Errorf("the operator must be told the hook failed, got %q", notices)
	}
}

func TestHookThatTimesOutWarnsAndDoesNotBlock(t *testing.T) {
	runner, _, rec := hookRunner(t, config.Hook{
		Event: config.HookPreTool, Command: shellSleep(), TimeoutSecs: 1,
	})

	result := runner.execute(context.Background(), writeCall("slow.txt"))

	if result.IsError {
		t.Fatalf("a timed out hook must not block the call: %q", result.Output)
	}
	if !strings.Contains(strings.Join(hookNotices(rec), "\n"), "timed out") {
		t.Errorf("a timeout must be reported, got %q", hookNotices(rec))
	}
}

func TestPostToolHookFeedbackReachesTheModel(t *testing.T) {
	runner, _, _ := hookRunner(t, config.Hook{
		Event: config.HookPostTool, Matcher: "write_file", Command: shellSay("lint: unused variable x", 2), TimeoutSecs: 30,
	})

	result := runner.execute(context.Background(), writeCall("lint.txt"))

	if result.IsError {
		t.Fatalf("post_tool feedback must not turn a success into a failure: %q", result.Output)
	}
	if !strings.Contains(result.Output, "[hook feedback] lint: unused variable x") {
		t.Errorf("feedback must be appended to the result, got %q", result.Output)
	}
}

func TestHookReceivesTheCallAsJSONOnStdin(t *testing.T) {
	runner, env, _ := hookRunner(t, config.Hook{
		Event: config.HookPostTool, Command: shellCapturePayload(), TimeoutSecs: 30,
	})

	runner.execute(context.Background(), writeCall("payload-target.txt"))

	data, err := os.ReadFile(filepath.Join(env.Workspace, "payload.json"))
	if err != nil {
		t.Fatalf("the hook did not see a payload: %v", err)
	}
	var payload struct {
		Event     string `json:"event"`
		Tool      string `json:"tool"`
		Workspace string `json:"workspace"`
		Args      struct {
			Path string `json:"path"`
		} `json:"args"`
		IsError *bool `json:"isError"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, data)
	}
	if payload.Event != config.HookPostTool || payload.Tool != "write_file" || payload.Args.Path != "payload-target.txt" {
		t.Errorf("payload = %+v, want the event, tool and arguments", payload)
	}
	if payload.Workspace != env.Workspace {
		t.Errorf("workspace = %q, want %q", payload.Workspace, env.Workspace)
	}
	if payload.IsError == nil || *payload.IsError {
		t.Errorf("a post_tool payload must carry isError=false for a success, got %v", payload.IsError)
	}
}

// A model-chosen argument must never become shell syntax: it travels on stdin
// and the command line is exactly what the operator wrote.
func TestHookCommandLineNeverContainsModelArguments(t *testing.T) {
	marker := "pwned-marker.txt"
	runner, env, _ := hookRunner(t, config.Hook{
		Event: config.HookPreTool, Command: shellSay("fine", 0), TimeoutSecs: 30,
	})
	call := provider.ToolCall{ID: "c1", Name: "write_file", Arguments: `{"path":"safe.txt; touch ` + marker + `","content":"x"}`}

	runner.execute(context.Background(), call)

	if _, err := os.Stat(filepath.Join(env.Workspace, marker)); err == nil {
		t.Fatal("a model argument was executed by the hook shell")
	}
}

func TestHookMessageFallsBackToStdoutAndClipsOnRuneBoundary(t *testing.T) {
	if got := hookMessage("", "from stdout\n"); got != "from stdout" {
		t.Errorf("hookMessage fell back to %q, want the stdout text", got)
	}
	long := strings.Repeat("\u00e9", hookMessageMax)
	got := hookMessage(long, "")
	if len(got) > hookMessageMax {
		t.Errorf("message length %d exceeds the cap %d", len(got), hookMessageMax)
	}
	if !utf8.ValidString(got) {
		t.Error("clipping must land on a rune boundary")
	}
}
