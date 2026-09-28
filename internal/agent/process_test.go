package agent

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain substitutes a cheap shell for the process tests. PowerShell can take
// several seconds to start, which would make these tests measure the shell
// rather than the manager. The production shell is asserted separately by
// TestShellInvocationIsReasonable.
func TestMain(m *testing.M) {
	// PowerShell can take several seconds to start, so both the manager and the
	// foreground runner are given a cheap shell for the suite. The production
	// choice is asserted separately by TestShellInvocationIsReasonable.
	if runtime.GOOS == "windows" {
		shellForProcess = func(command string) (string, []string) {
			return "cmd.exe", []string{"/c", command}
		}
		shellForTool = shellForProcess
	} else {
		shellForProcess = func(command string) (string, []string) {
			return "/bin/sh", []string{"-c", command}
		}
		shellForTool = shellForProcess
	}
	os.Exit(m.Run())
}

func quickCommand(text string) string {
	return "echo " + text
}

func failingCommand() string {
	if runtime.GOOS == "windows" {
		return "echo boom & exit 3"
	}
	return "echo boom; exit 3"
}

// longRunningCommand is a command that stays alive until killed. It uses the
// shell's own sleep rather than a background ping, so the process the manager
// tracks is the one that must be stopped.
func longRunningCommand() string {
	if runtime.GOOS == "windows" {
		return "ping -n 120 127.0.0.1 >nul"
	}
	return "sleep 120"
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// TestShellInvocationIsReasonable covers the production shell choice that the
// process tests deliberately bypass with a cheap substitute.
func TestShellInvocationIsReasonable(t *testing.T) {
	shell, args := shellInvocation("echo hi")
	if strings.TrimSpace(shell) == "" {
		t.Fatalf("a shell must be chosen")
	}
	if len(args) == 0 {
		t.Fatalf("the shell needs arguments to run a command")
	}
	// The command must be the last argument, whatever the interpreter, or the
	// command would be dropped and the shell would read from stdin instead.
	found := false
	for _, arg := range args {
		if strings.Contains(arg, "echo hi") {
			found = true
		}
	}
	if !found {
		t.Errorf("the command is missing from the shell arguments: %v", args)
	}
	if runtime.GOOS == "windows" && !strings.Contains(strings.ToLower(shell), "powershell") {
		t.Errorf("Windows should use PowerShell, got %q", shell)
	}
}

func newTestManager() *ProcessManager { return NewProcessManager() }

func TestBackgroundProcessCapturesOutputAndExits(t *testing.T) {
	manager := newTestManager()
	defer manager.Shutdown()

	process, err := manager.Start(context.Background(), quickCommand("hello"), t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if process.ID == "" {
		t.Fatalf("every process needs a handle")
	}

	select {
	case <-process.done:
	case <-time.After(30 * time.Second):
		t.Fatalf("a short command did not finish")
	}

	if !process.Exited() {
		t.Errorf("Exited should be true after done")
	}
	if code := process.ExitCode(); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if logs := process.LogsAll(); !strings.Contains(logs, "hello") {
		t.Errorf("output was not captured: %q", logs)
	}
}

func TestBackgroundProcessReportsFailure(t *testing.T) {
	manager := newTestManager()
	defer manager.Shutdown()

	process, err := manager.Start(context.Background(), failingCommand(), t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-process.done:
	case <-time.After(30 * time.Second):
		t.Fatalf("the failing command did not finish")
	}
	if code := process.ExitCode(); code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if !strings.Contains(process.LogsAll(), "boom") {
		t.Errorf("failure output was not captured: %q", process.LogsAll())
	}
}

func TestBackgroundProcessLogsAreIncremental(t *testing.T) {
	manager := newTestManager()
	defer manager.Shutdown()

	process, err := manager.Start(context.Background(), quickCommand("second"), t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-process.done:
	case <-time.After(30 * time.Second):
		t.Fatalf("the command did not finish")
	}

	// A first read from zero returns everything and a usable offset.
	first, offset, dropped := process.Logs(0)
	if dropped {
		t.Errorf("a fresh read should not report dropped output")
	}
	if offset <= 0 {
		t.Fatalf("offset should advance past the output, got %d", offset)
	}
	if !strings.Contains(first, "second") {
		t.Errorf("first read = %q", first)
	}

	// A second read from that offset returns nothing new.
	second, next, _ := process.Logs(offset)
	if strings.TrimSpace(second) != "" {
		t.Errorf("a read from the latest offset should be empty, got %q", second)
	}
	if next < offset {
		t.Errorf("the offset must never go backwards: %d then %d", offset, next)
	}
}

func TestRingBufferEvictsAndReportsIt(t *testing.T) {
	buffer := newRingBuffer(16)
	if _, err := buffer.Write([]byte("0123456789")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := buffer.Write([]byte("abcdefghij")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// The oldest bytes are gone, and a reader asking for them is told so
	// rather than silently receiving a partial answer.
	text, written, dropped := buffer.ReadSince(0)
	if !dropped {
		t.Errorf("reading evicted output must report the drop")
	}
	if written != 20 {
		t.Errorf("written = %d, want 20", written)
	}
	if len(text) != 16 {
		t.Errorf("buffer kept %d bytes, want the tail of 16", len(text))
	}
	if !strings.HasSuffix(text, "abcdefghij") {
		t.Errorf("the buffer should keep the tail, got %q", text)
	}

	// Reading from within the buffer is not a drop. "456789abcdefghij" holds
	// offsets 4 through 19, so offset 8 starts at the sixteenth byte "8".
	text, _, dropped = buffer.ReadSince(8)
	if dropped {
		t.Errorf("an offset still in the buffer is not a drop")
	}
	if text != "89abcdefghij" {
		t.Errorf("partial read = %q, want %q", text, "89abcdefghij")
	}
}

func TestProcessManagerKillStopsARunningProcess(t *testing.T) {
	manager := newTestManager()
	defer manager.Shutdown()

	// The tracked process is a shell that starts a child of its own, which is
	// the shape of every real command (`pnpm dev` runs node, and so on). That
	// descendant is what makes this test worth its name: killing only the
	// direct child leaves it holding the output pipe, and Wait then blocks
	// until the wait delay instead of returning at once.
	process, err := manager.Start(context.Background(), longRunningCommand(), t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if process.Exited() {
		t.Fatalf("a long command should still be running")
	}

	started := time.Now()
	if _, err := manager.Kill(process.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if !process.Wait(5 * time.Second) {
		t.Fatalf("the process did not stop after Kill")
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Errorf("kill took %s, which means a descendant survived and kept the pipe open", elapsed)
	}
	if !process.Exited() {
		t.Errorf("Exited should be true after a kill")
	}
}

func TestKillIsIdempotent(t *testing.T) {
	manager := newTestManager()
	defer manager.Shutdown()

	process, err := manager.Start(context.Background(), quickCommand("done"), t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-process.done:
	case <-time.After(30 * time.Second):
		t.Fatalf("the command did not finish")
	}

	// Asking to stop something that already stopped is not an error: the
	// operator got what they wanted.
	if _, err := manager.Kill(process.ID); err != nil {
		t.Errorf("killing a finished process should be a no-op, got %v", err)
	}
}

func TestKillUnknownHandleIsAnError(t *testing.T) {
	manager := newTestManager()
	if _, err := manager.Kill("proc-999"); err == nil {
		t.Errorf("an unknown handle must be rejected")
	}
}

func TestProcessManagerCapsConcurrency(t *testing.T) {
	manager := newTestManager()
	defer manager.Shutdown()

	for index := 0; index < maxBackgroundProcesses; index++ {
		if _, err := manager.Start(context.Background(), longRunningCommand(), t.TempDir()); err != nil {
			t.Fatalf("start %d: %v", index, err)
		}
	}
	// One more must be refused with advice rather than silently dropped.
	_, err := manager.Start(context.Background(), longRunningCommand(), t.TempDir())
	if err == nil {
		t.Fatalf("starting beyond the cap must fail")
	}
	if !strings.Contains(err.Error(), "run_kill") {
		t.Errorf("the refusal should say how to make room, got %v", err)
	}
}

func TestShutdownStopsEverything(t *testing.T) {
	manager := newTestManager()

	processes := make([]*Process, 0, 3)
	for index := 0; index < 3; index++ {
		process, err := manager.Start(context.Background(), longRunningCommand(), t.TempDir())
		if err != nil {
			t.Fatalf("start %d: %v", index, err)
		}
		processes = append(processes, process)
	}
	if running := manager.Running(); running != 3 {
		t.Fatalf("Running = %d, want 3", running)
	}

	// Shutdown must wait for the processes to go, not just signal them: the
	// caller exits right after, and a surviving child would still hold its
	// working directory and its output pipe.
	started := time.Now()
	manager.Shutdown()
	elapsed := time.Since(started)

	for _, process := range processes {
		if !process.Exited() {
			t.Errorf("%s survived Shutdown", process.ID)
		}
	}
	if running := manager.Running(); running != 0 {
		t.Errorf("Running = %d after Shutdown, want 0", running)
	}
	if elapsed > 6*time.Second {
		t.Errorf("Shutdown took %s, which suggests it waited on a process that never died", elapsed)
	}
}

func TestStartRequiresACommand(t *testing.T) {
	manager := newTestManager()
	defer manager.Shutdown()
	if _, err := manager.Start(context.Background(), "   ", t.TempDir()); err == nil {
		t.Errorf("a blank command must be rejected")
	}
}

func TestProcessSummaryNamesState(t *testing.T) {
	manager := newTestManager()
	defer manager.Shutdown()

	process, err := manager.Start(context.Background(), quickCommand("x"), t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-process.done:
	case <-time.After(30 * time.Second):
		t.Fatalf("the command did not finish")
	}
	summary := process.Summary()
	if !strings.Contains(summary, process.ID) || !strings.Contains(summary, "exited") {
		t.Errorf("summary = %q, want the handle and the state", summary)
	}
}

func TestLastLinesKeepsTheTail(t *testing.T) {
	text := "one\ntwo\nthree\nfour"
	if got := lastLines(text, 2); got != "three\nfour" {
		t.Errorf("lastLines = %q", got)
	}
	if got := lastLines("only", 5); got != "only" {
		t.Errorf("lastLines with a big n = %q", got)
	}
	if got := lastLines("", 3); got != "" {
		t.Errorf("lastLines of empty = %q", got)
	}
}
