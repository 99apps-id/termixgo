package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// bigOutput writes about 3000 numbered lines, far past the cap, and returns the
// command that prints them. A file and cat/type work in every shell the tool
// can pick, which a loop would not.
func bigOutput(t *testing.T, env *Env) string {
	t.Helper()
	var builder strings.Builder
	for line := 1; line <= 3000; line++ {
		fmt.Fprintf(&builder, "line %d padding-padding-padding-padding\n", line)
	}
	if err := os.WriteFile(filepath.Join(env.Workspace, "big.txt"), []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return "type big.txt"
	}
	return "cat big.txt"
}

// A command whose output is cut keeps the whole of it in the workspace and says
// where, and read_file can reach the part that was omitted.
func TestLongCommandOutputIsSavedAndReadable(t *testing.T) {
	env := testEnv(t)
	result, err := (&runCommandTool{}).Run(context.Background(), env, map[string]any{"command": bigOutput(t, env)})
	if err != nil || result.IsError {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	if !strings.Contains(result.Output, "omitted") {
		t.Fatalf("the output should have been clipped:\n%s", result.Output[:min(len(result.Output), 300)])
	}
	index := strings.Index(result.Output, "is saved at ")
	if index < 0 {
		t.Fatalf("the result should name the saved copy:\n%s", result.Output[max(0, len(result.Output)-400):])
	}
	rest := result.Output[index+len("is saved at "):]
	path := strings.TrimSpace(rest[:strings.Index(rest, ". Read")])
	if !strings.HasPrefix(path, ".termixgo/tool-output/") {
		t.Fatalf("saved at %q, want the workspace tool-output folder", path)
	}

	saved, err := os.ReadFile(filepath.Join(env.Workspace, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("the saved copy cannot be read: %v", err)
	}
	// The middle that the clipped result dropped is in the saved copy.
	if !strings.Contains(string(saved), "line 1500 ") || strings.Contains(result.Output, "line 1500 ") {
		t.Fatalf("line 1500 should be in the saved copy and not in the clipped result")
	}

	read, err := (&readFileTool{}).Run(context.Background(), env, map[string]any{"path": path, "offset": 1500, "limit": 3})
	if err != nil || read.IsError || !strings.Contains(read.Output, "line 1500 ") {
		t.Fatalf("read_file could not reach the omitted part: %+v err=%v", read, err)
	}
}

func TestShortOutputIsNotSaved(t *testing.T) {
	env := testEnv(t)
	result, _ := (&runCommandTool{}).Run(context.Background(), env, map[string]any{"command": quickCommand("hi")})
	if strings.Contains(result.Output, "is saved at") {
		t.Fatalf("a short output has nothing to save:\n%s", result.Output)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace, ".termixgo", "tool-output")); err == nil {
		t.Fatalf("no folder should be created for a short output")
	}
}

func TestSpillWithoutAWorkspaceKeepsTheClippedResult(t *testing.T) {
	if path := spillToolOutput(&Env{}, "run_command", "something"); path != "" {
		t.Fatalf("path = %q, want none without a workspace", path)
	}
	if got := appendSpill(&Env{}, "run_command", "full", "clipped"); got != "clipped" {
		t.Fatalf("appendSpill = %q, want the clipped text unchanged", got)
	}
}

// The folder is bounded: old files go, and past the count the oldest go first.
func TestSavedOutputsArePruned(t *testing.T) {
	env := testEnv(t)
	dir := filepath.Join(env.Workspace, filepath.FromSlash(toolOutputDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	aged := time.Now().Add(-toolOutputMaxAge - time.Hour)
	if err := os.Chtimes(old, aged, aged); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < toolOutputKeep+10; index++ {
		path := filepath.Join(dir, fmt.Sprintf("f%03d.txt", index))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-time.Duration(toolOutputKeep+10-index) * time.Minute)
		_ = os.Chtimes(path, when, when)
	}
	pruneToolOutput(dir)
	entries, _ := os.ReadDir(dir)
	if len(entries) != toolOutputKeep {
		t.Fatalf("%d files left, want %d", len(entries), toolOutputKeep)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatalf("a file past the age limit must be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "f000.txt")); err == nil {
		t.Fatalf("the oldest file past the count must be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("f%03d.txt", toolOutputKeep+9))); err != nil {
		t.Fatalf("the newest file must stay: %v", err)
	}
}

func TestSafeToolNameKeepsFileNamesPlain(t *testing.T) {
	if got := safeToolName("run/command: x"); got != "run_command__x" {
		t.Fatalf("safeToolName = %q", got)
	}
	if got := safeToolName(""); got != "tool" {
		t.Fatalf("safeToolName(empty) = %q", got)
	}
}

// The ring buffer reports what it evicted, so the saved copy can say that the
// earliest bytes are not in it.
func TestRingBufferAllReportsWhatWasEvicted(t *testing.T) {
	ring := newRingBuffer(10)
	_, _ = ring.Write([]byte("0123456789abcdefghijklmno"))
	text, evicted := ring.All()
	if text != "fghijklmno" || evicted != 15 {
		t.Fatalf("All() = %q, %d, want the last 10 bytes and 15 evicted", text, evicted)
	}
}
