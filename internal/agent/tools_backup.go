package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Edit backups are the safety net under every file write the agent performs.
// A bad edit is then one undo_edit call away instead of a git archaeology
// exercise, which matters most exactly where version control is absent: a new
// file nobody committed yet. The backups live under the workspace state
// directory, which grep, glob and the search index all skip, and checkpoint
// already excludes from its snapshots.
const (
	backupDirName      = "edit-backups"
	maxBackupsPerFile  = 5
	maxBackupFileBytes = 8 * 1024 * 1024
	absentMarkerExt    = ".absent"
	backupExt          = ".bak"
)

// backupFile records the current state of path before it is written. A file
// that does not exist yet leaves an absent marker, so undoing its creation
// removes it again. Keeping is bounded per file; failures are silent because
// a backup must never stop the write it protects.
func backupFile(env *Env, path string) {
	if env == nil || strings.TrimSpace(env.Workspace) == "" {
		return
	}
	rel, err := filepath.Rel(env.Workspace, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	root := filepath.Join(env.Workspace, ".termixgo", backupDirName, filepath.FromSlash(rel))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return
		}
		slot := filepath.Join(root, backupStamp()+absentMarkerExt)
		_ = os.WriteFile(slot, nil, 0o600)
		pruneBackups(root)
		return
	}
	if len(data) > maxBackupFileBytes {
		return
	}
	slot := filepath.Join(root, backupStamp()+backupExt)
	if err := os.WriteFile(slot, data, 0o600); err != nil {
		return
	}
	pruneBackups(root)
}

// backupStamp orders entries by name: zero-padded nanoseconds sort
// chronologically. The process id and a per-process counter keep two backups
// taken inside one clock tick apart: the Windows clock is too coarse to tell
// rapid successive writes apart on its own, and sharing a slot would lose the
// older backup.
var backupSeq atomic.Uint64

func backupStamp() string {
	return fmt.Sprintf("%019d-%d-%d", time.Now().UnixNano(), os.Getpid(), backupSeq.Add(1))
}

// pruneBackups keeps only the newest entries for one file.
func pruneBackups(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	if len(names) <= maxBackupsPerFile {
		return
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-maxBackupsPerFile] {
		_ = os.Remove(filepath.Join(root, name))
	}
}

// backupEntries lists one file's entries, newest first.
func backupEntries(env *Env, path string) ([]string, string, error) {
	rel, err := filepath.Rel(env.Workspace, path)
	if err != nil {
		return nil, "", err
	}
	root := filepath.Join(env.Workspace, ".termixgo", backupDirName, filepath.FromSlash(rel))
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, root, nil
		}
		return nil, root, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names, root, nil
}
