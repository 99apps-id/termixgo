package agent

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestStepToolsKeepsBackgroundToolsVisible pins the dev-server path under tool
// search: the background family must be advertised in every request, or the
// model never learns run_background exists and runs the server under
// run_command, where the timeout kills it.
func TestStepToolsKeepsBackgroundToolsVisible(t *testing.T) {
	visible := (&Runner{Tools: DefaultRegistry(), ToolSearch: true}).stepTools(nil)
	for _, name := range []string{"run_background", "run_logs", "run_wait", "run_list", "run_kill"} {
		if _, ok := visible.Lookup(name); !ok {
			t.Errorf("%s must stay visible with tool search on; a hidden dev-server tool is an undeployable one", name)
		}
	}
}

// TestTerminalFirstPrioritizesBackgroundTools pins the harness order: the
// profile that prefers shell work must actually rank the background tools
// first, by their real names.
func TestTerminalFirstPrioritizesBackgroundTools(t *testing.T) {
	profile := BuiltinHarnessProfiles["terminal_first"]
	ordered := ApplyHarnessToTools([]string{"read_file", "run_list", "run_command", "run_background", "edit"}, profile)
	for index, want := range []string{"run_command", "run_background", "run_list"} {
		if ordered[index] != want {
			t.Errorf("position %d = %q, want %q (full order %v)", index, ordered[index], want, ordered)
		}
	}
}

// TestLooksLikeServer pins the timeout hint gate: server startup wording
// earns the run_background suggestion, ordinary output does not.
func TestLooksLikeServer(t *testing.T) {
	for _, output := range []string{
		"Vite listening on http://localhost:5173",
		"Ready in 800 ms",
		"Server running at 127.0.0.1:3000",
		"Watching for file changes",
	} {
		if !looksLikeServer(output) {
			t.Errorf("looksLikeServer(%q) = false, want true", output)
		}
	}
	for _, output := range []string{"", "go test ./... ok", "error: module not found"} {
		if looksLikeServer(output) {
			t.Errorf("looksLikeServer(%q) = true, want false", output)
		}
	}
}

// serverThenHang prints startup wording and then stays alive, which is what a
// dev server looks like to the foreground runner: output first, exit never.
func serverThenHang() string {
	if runtime.GOOS == "windows" {
		return "echo listening on localhost:3000 & ping -n 30 127.0.0.1 >nul"
	}
	return "echo listening on localhost:3000; sleep 30"
}

func hangQuietly() string {
	if runtime.GOOS == "windows" {
		return "ping -n 30 127.0.0.1 >nul"
	}
	return "sleep 30"
}

// TestTimeoutSuggestsBackgroundForServers drives the real failure: a server
// started under run_command dies at the timeout, so the timeout message must
// point at run_background. A quiet hang must not get the same suggestion.
func TestTimeoutSuggestsBackgroundForServers(t *testing.T) {
	env := testEnv(t)
	timedOut, err := execute(context.Background(), env, serverThenHang(), env.Workspace, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !timedOut.IsError || !strings.Contains(timedOut.Output, "timed out") {
		t.Fatalf("expected a timeout, got %+v", timedOut)
	}
	if !strings.Contains(timedOut.Output, "run_background") {
		t.Errorf("a server killed by timeout must suggest run_background:\n%s", timedOut.Output)
	}

	quiet, err := execute(context.Background(), env, hangQuietly(), env.Workspace, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(quiet.Output, "run_background") {
		t.Errorf("a quiet hang is not a server, so it must not suggest run_background:\n%s", quiet.Output)
	}
}
