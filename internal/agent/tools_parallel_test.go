package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"path/filepath"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestParallelBatchStartsOneWorktreePerTask(t *testing.T) {
	workspace := worktreeRepo(t)
	manager := NewProcessManager()
	env := &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true, Processes: manager, Config: config.Default()}

	original := codeWorkerArgv
	codeWorkerArgv = func(_, _ string) ([]string, error) { return echoArgv("worked"), nil }
	defer func() { codeWorkerArgv = original }()

	result, err := (&parallelBatchTool{}).Run(context.Background(), env, map[string]any{
		"tasks": []any{
			map[string]any{"task": "fix login", "worker": "termixgo", "name": "batch-a"},
			map[string]any{"task": "fix logout", "worker": "termixgo", "name": "batch-b"},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("batch failed: %s", result.Output)
	}
	if !strings.Contains(result.Output, "2 of 2") {
		t.Errorf("summary should count both workers, got:\n%s", result.Output)
	}
	entries, err := loadWorktreeEntries(workspace)
	if err != nil || len(entries) != 2 {
		t.Fatalf("tracked worktrees = %+v, %v; want 2", entries, err)
	}
	for _, process := range manager.List() {
		process.Wait(15 * time.Second)
	}
}

func TestParallelBatchValidatesBeforeCreating(t *testing.T) {
	workspace := worktreeRepo(t)
	manager := NewProcessManager()
	env := &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true, Processes: manager, Config: config.Default()}

	result, _ := (&parallelBatchTool{}).Run(context.Background(), env, map[string]any{
		"tasks": []any{
			map[string]any{"task": "good task", "worker": "termixgo", "name": "ok-one"},
			map[string]any{"task": "bogus worker task", "worker": "bogus", "name": "bad-one"},
		},
	})
	if !result.IsError {
		t.Fatalf("a batch with an unknown worker must fail")
	}
	entries, _ := loadWorktreeEntries(workspace)
	if len(entries) != 0 {
		t.Errorf("no worktree should exist after validation fails, got %+v", entries)
	}
	var tooMany []any
	for i := 0; i < maxParallelBatch+1; i++ {
		tooMany = append(tooMany, map[string]any{"task": "task"})
	}
	capped, _ := (&parallelBatchTool{}).Run(context.Background(), env, map[string]any{"tasks": tooMany})
	if !capped.IsError {
		t.Errorf("a batch over the cap must be refused")
	}
}

func TestParallelBatchRefusesAMissingWorkerBinary(t *testing.T) {
	workspace := worktreeRepo(t)
	env := &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true, Processes: NewProcessManager(), Config: config.Default()}

	original := codeWorkerArgv
	codeWorkerArgv = func(_, _ string) ([]string, error) {
		return []string{"termixgo-worker-that-does-not-exist", "--task"}, nil
	}
	defer func() { codeWorkerArgv = original }()

	result, err := (&parallelBatchTool{}).Run(context.Background(), env, map[string]any{
		"tasks": []any{map[string]any{"task": "fix login", "worker": "claude", "name": "missing-one"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Output, "not installed") {
		t.Fatalf("a missing worker binary must be named, got %+v", result)
	}
	entries, _ := loadWorktreeEntries(workspace)
	if len(entries) != 0 {
		t.Errorf("a refused batch must leave no worktree behind, got %+v", entries)
	}
}

func TestParallelBatchLabelNamesTheWorkers(t *testing.T) {
	label := (&parallelBatchTool{}).Label(map[string]any{
		"tasks": []any{
			map[string]any{"task": "a", "worker": "codex"},
			map[string]any{"task": "b", "worker": "codex"},
			map[string]any{"task": "c", "worker": "termixgo"},
		},
	})
	if !strings.Contains(label, "3 parallel workers") {
		t.Errorf("the label should count the batch, got %q", label)
	}
	for _, want := range []string{"codex", "termixgo"} {
		if !strings.Contains(label, want) {
			t.Errorf("the label should name %s so the approval says what it starts, got %q", want, label)
		}
	}
}

func TestParallelBatchRefusesDuplicateNames(t *testing.T) {
	workspace := worktreeRepo(t)
	env := &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true, Processes: NewProcessManager(), Config: config.Default()}

	original := codeWorkerArgv
	codeWorkerArgv = func(_, _ string) ([]string, error) { return echoArgv("worked"), nil }
	defer func() { codeWorkerArgv = original }()

	result, err := (&parallelBatchTool{}).Run(context.Background(), env, map[string]any{
		"tasks": []any{
			map[string]any{"task": "fix login", "worker": "termixgo", "name": "same"},
			map[string]any{"task": "fix logout", "worker": "termixgo", "name": "same"},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Output, "worktree name") {
		t.Fatalf("two tasks claiming one checkout must be refused, got %+v", result)
	}
	entries, _ := loadWorktreeEntries(workspace)
	if len(entries) != 0 {
		t.Errorf("a refused batch must leave no worktree behind, got %+v", entries)
	}
}

func TestBatchStatusNamesRunningAndDone(t *testing.T) {
	workspace := worktreeRepo(t)
	manager := NewProcessManager()
	now := time.Now()
	if err := recordWorktree(workspace, worktreeEntry{Name: "alpha", Path: filepath.Join(workspace, "..", "alpha"), Branch: "termixgo/alpha", CreatedAt: now, LastUsedAt: now}); err != nil {
		t.Fatalf("record: %v", err)
	}
	text := batchStatus(workspace, manager, now)
	if !strings.Contains(text, "alpha") || !strings.Contains(text, "idle") {
		t.Errorf("status should name the idle worktree, got:\n%s", text)
	}
}
