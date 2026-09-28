package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// ------------------------------------------------- shell selection

// TestPosixShellPrefersBashThenFallsBack pins both branches of the POSIX path,
// which the Windows machine running this suite would otherwise never execute.
// The lookup is injected, so the decision is tested rather than the platform.
func TestPosixShellPrefersBashThenFallsBack(t *testing.T) {
	command := "echo hi"

	bash, args := posixShell(command, func(name string) (string, error) {
		if name != "bash" {
			t.Fatalf("looked up %q, want bash", name)
		}
		return "/usr/local/bin/bash", nil
	})
	if bash != "/usr/local/bin/bash" {
		t.Errorf("shell = %q, want the bash that was found", bash)
	}
	if len(args) != 2 || args[0] != "-lc" || args[1] != command {
		t.Errorf("args = %v, want a login shell invocation", args)
	}

	missing, args := posixShell(command, func(string) (string, error) {
		return "", os.ErrNotExist
	})
	if missing != "/bin/sh" {
		t.Errorf("shell = %q, want the sh fallback", missing)
	}
	if len(args) != 2 || args[0] != "-c" || args[1] != command {
		t.Errorf("args = %v, want the sh invocation", args)
	}
}

// TestWindowsShellRunsNonInteractively is the flag set that keeps a profile
// from changing what a command does.
func TestWindowsShellRunsNonInteractively(t *testing.T) {
	command := "echo hi"
	args := windowsShell(command)
	if args[len(args)-1] != command {
		t.Errorf("args = %v, want the command last", args)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args = %v, want %s", args, want)
		}
	}
}

// ------------------------------------------------- filesystem tool branches

func TestReadFileExplainsWhyItRefused(t *testing.T) {
	env := testEnv(t)
	if err := os.MkdirAll(filepath.Join(env.Workspace, "adir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	png := filepath.Join(env.Workspace, "logo.png")
	if err := os.WriteFile(png, []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := &readFileTool{}

	missing, err := tool.Run(context.Background(), env, map[string]any{"path": "nope.go"})
	if err != nil || !missing.IsError {
		t.Fatalf("a missing file must be reported: err=%v result=%+v", err, missing)
	}
	if !strings.Contains(missing.Output, "does not exist") {
		t.Errorf("output = %q", missing.Output)
	}

	// A directory gets the tool that actually helps rather than a wall of nil.
	directory, err := tool.Run(context.Background(), env, map[string]any{"path": "adir"})
	if err != nil || !directory.IsError {
		t.Fatalf("a directory must be refused: err=%v result=%+v", err, directory)
	}
	if !strings.Contains(directory.Output, "list_directory") {
		t.Errorf("output = %q, want it to name the right tool", directory.Output)
	}

	// A binary is refused rather than rendered as mojibake into the context.
	binary, err := tool.Run(context.Background(), env, map[string]any{"path": "logo.png"})
	if err != nil || !binary.IsError {
		t.Fatalf("a binary must be refused: err=%v result=%+v", err, binary)
	}
	if !strings.Contains(binary.Output, "binary") {
		t.Errorf("output = %q", binary.Output)
	}

	if _, err := tool.Run(context.Background(), env, map[string]any{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestReadFileWindowReportsWhatItLeftOut is what lets the model ask for the
// next page instead of concluding the file ends there.
func TestReadFileWindowReportsWhatItLeftOut(t *testing.T) {
	env := testEnv(t)
	var lines []string
	for index := 1; index <= 40; index++ {
		lines = append(lines, "line "+itoa(index))
	}
	path := filepath.Join(env.Workspace, "long.txt")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := &readFileTool{}

	first, err := tool.Run(context.Background(), env, map[string]any{"path": "long.txt", "limit": 10})
	if err != nil || first.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, first)
	}
	if !strings.Contains(first.Output, "lines 1-10 of 40") {
		t.Errorf("output = %q, want the window header", first.Output)
	}
	if !strings.Contains(first.Output, "next offset 11") {
		t.Errorf("output = %q, want the continuation hint", first.Output)
	}

	second, err := tool.Run(context.Background(), env, map[string]any{"path": "long.txt", "offset": 11, "limit": 10})
	if err != nil || second.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, second)
	}
	if !strings.Contains(second.Output, "lines 11-20 of 40") {
		t.Errorf("output = %q", second.Output)
	}

	// An offset past the end is clamped rather than slicing out of range.
	beyond, err := tool.Run(context.Background(), env, map[string]any{"path": "long.txt", "offset": 9999})
	if err != nil || beyond.IsError {
		t.Fatalf("an offset past the end must not fail: err=%v result=%+v", err, beyond)
	}
	if !strings.Contains(beyond.Output, "of 40") {
		t.Errorf("output = %q", beyond.Output)
	}
}

func TestReadFileClipsAVeryLongWindow(t *testing.T) {
	env := testEnv(t)
	// One line far bigger than the byte cap: a minified file or a fixture.
	huge := strings.Repeat("x", maxReadBytes+5000)
	if err := os.WriteFile(filepath.Join(env.Workspace, "big.txt"), []byte(huge), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := (&readFileTool{}).Run(context.Background(), env, map[string]any{"path": "big.txt"})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Output, "clipped at 64 KB") {
		t.Errorf("output should say it was clipped")
	}
	if len(result.Output) > maxReadBytes+200 {
		t.Errorf("output is %d bytes, want it bounded", len(result.Output))
	}
}

func TestListDirectoryCapsAndReportsAnEmptyFolder(t *testing.T) {
	env := testEnv(t)
	empty := filepath.Join(env.Workspace, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tool := &listDirectoryTool{}

	lonely, err := tool.Run(context.Background(), env, map[string]any{"path": "empty"})
	if err != nil || lonely.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, lonely)
	}
	if !strings.Contains(lonely.Output, "(empty)") {
		t.Errorf("output = %q, want the empty marker", lonely.Output)
	}

	// A directory that does not exist is an error the model can act on.
	if _, err := tool.Run(context.Background(), env, map[string]any{"path": "ghost"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	missing, _ := tool.Run(context.Background(), env, map[string]any{"path": "ghost"})
	if !missing.IsError {
		t.Errorf("a missing directory must be reported, got %q", missing.Output)
	}

	// The cap keeps one listing from filling the context window.
	crowded := filepath.Join(env.Workspace, "crowded")
	if err := os.MkdirAll(crowded, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for index := 0; index < 520; index++ {
		name := filepath.Join(crowded, "f"+itoa(index)+".txt")
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	capped, err := tool.Run(context.Background(), env, map[string]any{"path": "crowded"})
	if err != nil || capped.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, capped)
	}
	if !strings.Contains(capped.Output, "... and 20 more") {
		t.Errorf("output = %q, want the remainder counted", capped.Output)
	}
}

func TestWriteFileRequiresContentAndCreatesParents(t *testing.T) {
	env := testEnv(t)
	tool := &writeFileTool{}

	if _, err := tool.Run(context.Background(), env, map[string]any{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	noPath, _ := tool.Run(context.Background(), env, map[string]any{"content": "x"})
	if !noPath.IsError || !strings.Contains(noPath.Output, "path is required") {
		t.Errorf("output = %q", noPath.Output)
	}
	noContent, _ := tool.Run(context.Background(), env, map[string]any{"path": "a.go"})
	if !noContent.IsError || !strings.Contains(noContent.Output, "content is required") {
		t.Errorf("output = %q", noContent.Output)
	}

	// A nested path is created without a separate step, which is what makes a
	// new package a single call.
	nested, err := tool.Run(context.Background(), env, map[string]any{
		"path": "internal/newpkg/new.go", "content": "package newpkg\n",
	})
	if err != nil || nested.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, nested)
	}
	if !strings.Contains(nested.Output, "internal/newpkg/new.go") {
		t.Errorf("output = %q, want the relative path", nested.Output)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace, "internal", "newpkg", "new.go")); err != nil {
		t.Errorf("the file was not written: %v", err)
	}

	// A non-string content value is coerced rather than refused, because a
	// model often sends an object or a number.
	coerced, err := tool.Run(context.Background(), env, map[string]any{"path": "data.json", "content": map[string]any{"a": 1}})
	if err != nil || coerced.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, coerced)
	}
	written, _ := os.ReadFile(filepath.Join(env.Workspace, "data.json"))
	if !strings.Contains(string(written), `"a":1`) {
		t.Errorf("file = %q, want the marshalled value", written)
	}
}

func TestCreateDirectoryReportsAnExistingFolder(t *testing.T) {
	env := testEnv(t)
	tool := &createDirectoryTool{}

	if _, err := tool.Run(context.Background(), env, map[string]any{"path": "made"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	first, err := tool.Run(context.Background(), env, map[string]any{"path": "made"})
	if err != nil || first.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, first)
	}
	if !strings.Contains(first.Output, "already exists") {
		t.Errorf("output = %q, want it to say the folder was there", first.Output)
	}
	// A file in the way is a failure the model has to see.
	blocker := filepath.Join(env.Workspace, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tool.Run(context.Background(), env, map[string]any{"path": "blocked"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// ------------------------------------------------- edit tool branches

func TestEditRequiresPathOldStringAndAFile(t *testing.T) {
	env := testEnv(t)
	tool := &editTool{}
	path := filepath.Join(env.Workspace, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := tool.Run(context.Background(), env, map[string]any{"old_string": "x", "new_string": "y"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	noPath, _ := tool.Run(context.Background(), env, map[string]any{"old_string": "x", "new_string": "y"})
	if !noPath.IsError || !strings.Contains(noPath.Output, "path is required") {
		t.Errorf("output = %q", noPath.Output)
	}

	noOld, _ := tool.Run(context.Background(), env, map[string]any{"path": "main.go", "new_string": "y"})
	if !noOld.IsError || !strings.Contains(noOld.Output, "old_string is required") {
		t.Errorf("output = %q", noOld.Output)
	}

	// The alias spelling is accepted, because a model trained on another tool
	// sends "old" and "new".
	aliased, err := tool.Run(context.Background(), env, map[string]any{"path": "main.go", "old": "func main() {}", "new": "func run() {}"})
	if err != nil || aliased.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, aliased)
	}
	updated, _ := os.ReadFile(path)
	if !strings.Contains(string(updated), "func run() {}") {
		t.Errorf("file = %q", updated)
	}
}

// TestMultiEditRefusesAnEmptyOrUnusableBatch is the atomicity contract on the
// input side: nothing is written unless every replacement is given.
func TestMultiEditRefusesAnEmptyOrUnusableBatch(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env.Workspace, "main.go")
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := &multiEditTool{}

	if _, err := tool.Run(context.Background(), env, map[string]any{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	noPath, _ := tool.Run(context.Background(), env, map[string]any{"edits": []any{}})
	if !noPath.IsError || !strings.Contains(noPath.Output, "path is required") {
		t.Errorf("output = %q", noPath.Output)
	}

	notAnArray, _ := tool.Run(context.Background(), env, map[string]any{"path": "main.go", "edits": "nope"})
	if !notAnArray.IsError || !strings.Contains(notAnArray.Output, "array") {
		t.Errorf("output = %q", notAnArray.Output)
	}

	empty, _ := tool.Run(context.Background(), env, map[string]any{"path": "main.go", "edits": []any{}})
	if !empty.IsError || !strings.Contains(empty.Output, "at least one") {
		t.Errorf("output = %q", empty.Output)
	}

	// A batch with a malformed entry is refused before anything is written.
	malformed, _ := tool.Run(context.Background(), env, map[string]any{
		"path":  "main.go",
		"edits": []any{"a bare string"},
	})
	if !malformed.IsError {
		t.Errorf("a malformed edit must be refused, got %q", malformed.Output)
	}

	// And a batch where one replacement cannot match leaves the file alone.
	unmatched, _ := tool.Run(context.Background(), env, map[string]any{
		"path": "main.go",
		"edits": []any{
			map[string]any{"old_string": "package main", "new_string": "package other"},
			map[string]any{"old_string": "not in the file", "new_string": "x"},
		},
	})
	if !unmatched.IsError {
		t.Fatalf("an unmatched replacement must abort the batch, got %q", unmatched.Output)
	}
	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Errorf("the file was modified by a failed batch:\n%s", after)
	}
}

// ------------------------------------------------- other tool branches

// TestRememberReportsMissingSupport keeps a restricted session from looking
// like a successful write.
func TestRememberReportsMissingSupport(t *testing.T) {
	env := testEnv(t)
	env.Memory = nil

	result, err := (&rememberTool{}).Run(context.Background(), env, map[string]any{"fact": "something"})
	if err != nil || !result.IsError {
		t.Fatalf("a session without memory must say so: err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Output, "memory") {
		t.Errorf("output = %q", result.Output)
	}
	if _, err := (&rememberTool{}).Run(context.Background(), env, map[string]any{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	empty, _ := (&rememberTool{}).Run(context.Background(), env, map[string]any{"fact": "  "})
	if !empty.IsError || !strings.Contains(empty.Output, "fact is required") {
		t.Errorf("output = %q", empty.Output)
	}
}

// TestRememberReportsAnUnwritableLocation covers the scope whose file cannot be
// created, which must not read as a stored fact.
func TestRememberReportsAnUnwritableLocation(t *testing.T) {
	env := testEnv(t)
	// A file where the memory directory belongs makes the write impossible.
	blocker := filepath.Join(env.Workspace, ".termixgo")
	if err := os.WriteFile(blocker, []byte("in the way"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	result, err := (&rememberTool{}).Run(context.Background(), env, map[string]any{"fact": "a fact"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsError {
		t.Errorf("an unwritable memory location must be reported, got %q", result.Output)
	}
}

func TestGitToolsNeedARepositoryForEveryCommand(t *testing.T) {
	env := testEnv(t)
	tools := []Tool{
		&gitStatusTool{}, &gitDiffTool{}, &gitLogTool{}, &gitShowTool{},
		&gitAddTool{}, &gitCommitTool{}, &gitBranchTool{}, &gitRestoreTool{},
	}
	for _, tool := range tools {
		result, err := tool.Run(context.Background(), env, map[string]any{
			"message": "a message",
			"all":     true,
			"paths":   []any{"a.go"},
			"path":    "a.go",
			"branch":  "main",
		})
		if err != nil {
			t.Errorf("%s returned a Go error: %v", tool.Name(), err)
			continue
		}
		// Each one must refuse rather than run git in a folder that is not a
		// repository, which is what keeps the refusal message actionable.
		if !result.IsError {
			t.Errorf("%s should refuse outside a repository, got %q", tool.Name(), result.Output)
			continue
		}
		if !strings.Contains(result.Output, "git") {
			t.Errorf("%s output = %q, want it to mention git", tool.Name(), result.Output)
		}
	}
}

// ------------------------------------------------- history hints

func TestHistoryHintCoversTheEmptyAndFullCases(t *testing.T) {
	// No messages is the first turn, which must report a usable figure rather
	// than a division by zero.
	if got := HistoryHint(nil, 100000); strings.TrimSpace(got) == "" {
		t.Errorf("HistoryHint(nil) = %q, want a readable hint", got)
	}
	if got := HistoryHint(nil, 0); strings.TrimSpace(got) == "" {
		t.Errorf("a zero window must still produce a hint, got %q", got)
	}
	// A single huge message exceeds the budget, which is the state the hint
	// exists to explain.
	huge := []provider.Message{{Role: provider.RoleUser, Content: strings.Repeat("x", 400000)}}
	over := HistoryHint(huge, 1000)
	if !strings.Contains(over, "tokens") {
		t.Errorf("HistoryHint = %q, want a token count", over)
	}
	small := []provider.Message{{Role: provider.RoleUser, Content: "hello"}}
	if got := HistoryHint(small, 100000); !strings.Contains(got, "tokens") {
		t.Errorf("HistoryHint = %q", got)
	}
}
