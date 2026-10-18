package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func undoEnv(t *testing.T) *Env {
	t.Helper()
	workspace := t.TempDir()
	return &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true}
}

func readWorkFile(t *testing.T, workspace, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workspace, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestUndoEditRoundTrip walks the whole safety net: create, change, revert
// the change, then revert the creation.
func TestUndoEditRoundTrip(t *testing.T) {
	env := undoEnv(t)
	write := &writeFileTool{}
	edit := &editTool{}
	undo := &undoEditTool{}
	ctx := context.Background()

	if result, _ := write.Run(ctx, env, map[string]any{"path": "note.txt", "content": "version one\n"}); result.IsError {
		t.Fatalf("write: %q", result.Output)
	}
	if result, _ := edit.Run(ctx, env, map[string]any{
		"path": "note.txt", "old_string": "version one\n", "new_string": "version two\n",
	}); result.IsError {
		t.Fatalf("edit: %q", result.Output)
	}
	if got := readWorkFile(t, env.Workspace, "note.txt"); got != "version two\n" {
		t.Fatalf("the edit did not apply, got %q", got)
	}

	if result, _ := undo.Run(ctx, env, map[string]any{"path": "note.txt"}); result.IsError {
		t.Fatalf("undo the edit: %q", result.Output)
	}
	if got := readWorkFile(t, env.Workspace, "note.txt"); got != "version one\n" {
		t.Errorf("undo must restore the previous content, got %q", got)
	}

	if result, _ := undo.Run(ctx, env, map[string]any{"path": "note.txt"}); result.IsError {
		t.Fatalf("undo the creation: %q", result.Output)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace, "note.txt")); !os.IsNotExist(err) {
		t.Errorf("undoing a creation must remove the file again")
	}

	// Nothing is left to undo now.
	if result, _ := undo.Run(ctx, env, map[string]any{"path": "note.txt"}); !result.IsError {
		t.Errorf("undo with no backups must fail")
	}
}

// TestUndoEditRestoresPatchDelete proves a file removed through apply_patch
// comes back with its content.
func TestUndoEditRestoresPatchDelete(t *testing.T) {
	env := undoEnv(t)
	if err := os.WriteFile(filepath.Join(env.Workspace, "old.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := &applyPatchTool{}
	document := "*** Begin Patch\n*** Delete File: old.txt\n*** End Patch\n"
	if result, _ := patch.Run(context.Background(), env, map[string]any{"patch": document}); result.IsError {
		t.Fatalf("delete: %q", result.Output)
	}
	undo := &undoEditTool{}
	if result, _ := undo.Run(context.Background(), env, map[string]any{"path": "old.txt"}); result.IsError {
		t.Fatalf("undo the delete: %q", result.Output)
	}
	if got := readWorkFile(t, env.Workspace, "old.txt"); got != "keep me\n" {
		t.Errorf("undo must restore the deleted content, got %q", got)
	}
}

// TestUndoEditTakesSteps reverts several writes at once and refuses more
// steps than there are backups.
func TestUndoEditTakesSteps(t *testing.T) {
	env := undoEnv(t)
	ctx := context.Background()
	write := &writeFileTool{}
	for _, content := range []string{"one\n", "two\n", "three\n"} {
		if result, _ := write.Run(ctx, env, map[string]any{"path": "count.txt", "content": content}); result.IsError {
			t.Fatalf("write: %q", result.Output)
		}
	}
	undo := &undoEditTool{}
	if result, _ := undo.Run(ctx, env, map[string]any{"path": "count.txt", "steps": 2}); result.IsError {
		t.Fatalf("undo two steps: %q", result.Output)
	}
	if got := readWorkFile(t, env.Workspace, "count.txt"); got != "one\n" {
		t.Errorf("two steps must reach the first content, got %q", got)
	}
	if result, _ := undo.Run(ctx, env, map[string]any{"path": "count.txt", "steps": 2}); !result.IsError {
		t.Errorf("more steps than backups must fail")
	}
}

// TestBackupPrunesOldEntries keeps the backup directory bounded no matter
// how often a file is rewritten.
func TestBackupPrunesOldEntries(t *testing.T) {
	env := undoEnv(t)
	ctx := context.Background()
	write := &writeFileTool{}
	for index := 0; index < maxBackupsPerFile+3; index++ {
		content := strings.Repeat("x", index+1) + "\n"
		if result, _ := write.Run(ctx, env, map[string]any{"path": "busy.txt", "content": content}); result.IsError {
			t.Fatalf("write %d: %q", index, result.Output)
		}
	}
	names, _, err := backupEntries(env, filepath.Join(env.Workspace, "busy.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != maxBackupsPerFile {
		t.Errorf("backups = %d, want the cap of %d", len(names), maxBackupsPerFile)
	}
}

// TestFailedEditLeavesNoBackup keeps a refused write from spending the one
// undo the operator may need for a real change.
func TestFailedEditLeavesNoBackup(t *testing.T) {
	env := undoEnv(t)
	if err := os.WriteFile(filepath.Join(env.Workspace, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := &editTool{}
	if result, _ := edit.Run(context.Background(), env, map[string]any{
		"path": "a.txt", "old_string": "missing", "new_string": "other",
	}); !result.IsError {
		t.Fatalf("the edit must fail")
	}
	names, _, err := backupEntries(env, filepath.Join(env.Workspace, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Errorf("a refused edit must leave no backup, got %d", len(names))
	}
}
