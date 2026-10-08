package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// undoEditTool restores a file to what it held before the last writes.
type undoEditTool struct{}

func (t *undoEditTool) Name() string      { return "undo_edit" }
func (t *undoEditTool) Aliases() []string { return []string{"undo"} }
func (t *undoEditTool) Mutating() bool    { return true }
func (t *undoEditTool) Risk() Risk        { return RiskEdit }
func (t *undoEditTool) Label(a map[string]any) string {
	return "Undoing " + displayName(a)
}
func (t *undoEditTool) DoneLabel(a map[string]any) string {
	return "Undid " + displayName(a)
}
func (t *undoEditTool) Description() string {
	return "Restore a file to what it held before the last edit. Every write_file, edit, multi_edit and apply_patch keeps a bounded backup, so one call reverts one write. Pass steps to revert several writes at once. Files removed or renamed by delete_file and move_file are not covered; use git or a checkpoint for those."
}
func (t *undoEditTool) Schema() map[string]any {
	return object(map[string]any{
		"path":  strProp("File to restore, absolute or relative to the workspace."),
		"steps": intProp("How many writes to revert, 1 to 5. Defaults to 1."),
	}, "path")
}

func (t *undoEditTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := argString(args, "path", "file")
	if raw == "" {
		return Result{Output: "path is required; pass the file to restore", IsError: true}, nil
	}
	path := resolvePath(env, raw)
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	steps := argInt(args, "steps", 1, 1, maxBackupsPerFile)
	names, root, err := backupEntries(env, path)
	if err != nil {
		return Result{Output: fmt.Sprintf("read backups: %v", err), IsError: true}, nil
	}
	if len(names) < steps {
		if len(names) == 0 {
			return Result{Output: fmt.Sprintf("no backups for %s; only files written by write_file, edit, multi_edit or apply_patch can be undone", displayPath(env, path)), IsError: true}, nil
		}
		return Result{Output: fmt.Sprintf("%s holds %d backup(s), which is fewer than the %d requested step(s)", displayPath(env, path), len(names), steps), IsError: true}, nil
	}
	// The restore bytes are read before the entries are consumed: the
	// target entry is deleted with the rest, so reading after would fail.
	target := names[steps-1]
	absent := strings.HasSuffix(target, absentMarkerExt)
	var data []byte
	if !absent {
		data, err = os.ReadFile(filepath.Join(root, target))
		if err != nil {
			return Result{Output: fmt.Sprintf("read backup: %v", err), IsError: true}, nil
		}
	}
	for _, name := range names[:steps] {
		_ = os.Remove(filepath.Join(root, name))
	}
	if absent {
		// The file did not exist before the write, so undoing removes it.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return Result{Output: fmt.Sprintf("remove %s: %v", displayPath(env, path), err), IsError: true}, nil
		}
		return Result{Output: fmt.Sprintf("Undid %d write(s) in %s: the file did not exist before, so it was removed", steps, displayPath(env, path))}, nil
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return Result{Output: fmt.Sprintf("restore %s: %v", displayPath(env, path), err), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Undid %d write(s) in %s", steps, displayPath(env, path))}, nil
}
