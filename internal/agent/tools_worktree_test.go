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

// ageWorktree backdates a registry entry so prune sees it as idle.
func ageWorktree(t *testing.T, workspace, name string) string {
	t.Helper()
	entries, err := loadWorktreeEntries(workspace)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no registry entries: %v", err)
	}
	path := ""
	for index := range entries {
		if entries[index].Name == name {
			entries[index].LastUsedAt = time.Now().Add(-48 * time.Hour)
			path = entries[index].Path
		}
	}
	if path == "" {
		t.Fatalf("no entry named %q in %v", name, entries)
	}
	if err := saveWorktreeEntries(workspace, entries); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorktreePruneReclaimsAnIdleCleanTree(t *testing.T) {
	workspace := worktreeRepo(t)
	env := worktreeEnv(t, workspace)
	tool := &gitWorktreeTool{}
	ctx := context.Background()

	if _, err := tool.Run(ctx, env, map[string]any{"action": "add", "path": "idle", "branch": "idle-feature"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	dir := ageWorktree(t, workspace, "idle")

	result, _ := tool.Run(ctx, env, map[string]any{"action": "prune", "max_age": "1h"})
	if result.IsError {
		t.Fatalf("prune is an error: %q", result.Output)
	}
	if !strings.Contains(result.Output, "Pruned") {
		t.Fatalf("prune output = %q", result.Output)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the idle worktree directory should be gone")
	}
	if entries, _ := loadWorktreeEntries(workspace); len(entries) != 0 {
		t.Errorf("the registry should be empty, got %v", entries)
	}
	refs, err := runGit(ctx, env, "for-each-ref", "--format=%(refname)", "refs/termixgo/snapshots")
	if err != nil || !strings.Contains(refs, "refs/termixgo/snapshots/") {
		t.Errorf("the tip should be snapshotted first, got %q err=%v", refs, err)
	}
}

func TestWorktreePruneKeepsADirtyTree(t *testing.T) {
	workspace := worktreeRepo(t)
	env := worktreeEnv(t, workspace)
	tool := &gitWorktreeTool{}
	ctx := context.Background()

	if _, err := tool.Run(ctx, env, map[string]any{"action": "add", "path": "busy", "branch": "busy-feature"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	dir := ageWorktree(t, workspace, "busy")
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, _ := tool.Run(ctx, env, map[string]any{"action": "prune", "max_age": "1h"})
	if !strings.Contains(result.Output, "Kept") || !strings.Contains(result.Output, "uncommitted") {
		t.Fatalf("prune should keep a dirty tree, got %q", result.Output)
	}
	if _, err := os.Stat(filepath.Join(dir, "scratch.txt")); err != nil {
		t.Errorf("the dirty worktree should still exist: %v", err)
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
