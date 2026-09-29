package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func worktreeRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "git", args...)
		command.Dir = dir
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "base")
	return dir
}

func worktreeEnv(t *testing.T, workspace string) *Env {
	t.Helper()
	return &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true}
}

func TestWorktreeAddListRemove(t *testing.T) {
	workspace := worktreeRepo(t)
	env := worktreeEnv(t, workspace)
	tool := &gitWorktreeTool{}
	ctx := context.Background()

	added, err := tool.Run(ctx, env, map[string]any{"action": "add", "path": "sibling", "branch": "feature"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if added.IsError {
		t.Fatalf("add is an error: %q", added.Output)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(workspace), "sibling", "base.txt")); err != nil {
		t.Errorf("the sibling checkout is missing base.txt: %v", err)
	}

	listed, _ := tool.Run(ctx, env, map[string]any{"action": "list"})
	if !strings.Contains(listed.Output, "sibling") || !strings.Contains(listed.Output, "feature") {
		t.Errorf("list should name the worktree and branch:\n%s", listed.Output)
	}

	removed, _ := tool.Run(ctx, env, map[string]any{"action": "remove", "path": "sibling"})
	if removed.IsError {
		t.Fatalf("remove is an error: %q", removed.Output)
	}
	again, _ := tool.Run(ctx, env, map[string]any{"action": "list"})
	if strings.Contains(again.Output, "sibling") {
		t.Errorf("the worktree should be gone:\n%s", again.Output)
	}
}

func TestWorktreeRequiresActionAndPath(t *testing.T) {
	workspace := worktreeRepo(t)
	env := worktreeEnv(t, workspace)
	tool := &gitWorktreeTool{}
	ctx := context.Background()
	for _, args := range []map[string]any{
		{"action": "sideways"},
		{"action": "add"},
		{"action": "remove"},
	} {
		result, _ := tool.Run(ctx, env, args)
		if !result.IsError {
			t.Errorf("args %v must fail", args)
		}
	}
}

func TestWorktreeOutsideGitFails(t *testing.T) {
	env := worktreeEnv(t, t.TempDir())
	tool := &gitWorktreeTool{}
	result, _ := tool.Run(context.Background(), env, map[string]any{"action": "list"})
	if !result.IsError {
		t.Errorf("outside git the tool must report an error")
	}
}

// TestSummarizeWorktreesIgnoresAPrefixLine keeps the listing alive when git
// prints a modifier before any worktree, which is what a bare repository does.
//
// The summary appends the branch and the bare marker to the previous row, so a
// modifier with no row before it has nothing to attach to. The guard is what
// makes that a no-op instead of an index out of range.
func TestSummarizeWorktreesIgnoresAPrefixLine(t *testing.T) {
	// A branch line with no worktree line before it must not panic.
	out := summarizeWorktrees("branch refs/heads/main\nworktree /tmp/repo\n")
	if !strings.Contains(out, "/tmp/repo") {
		t.Errorf("output missing the worktree path: %q", out)
	}
	if strings.Contains(out, "main") {
		t.Errorf("the orphan branch line should be ignored, got %q", out)
	}

	// A bare worktree with no branch: the marker attaches to its own row.
	out = summarizeWorktrees("worktree /tmp/bare\nbare\n")
	if !strings.Contains(out, "[bare]") {
		t.Errorf("output should mark the bare worktree: %q", out)
	}
}
