package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// worktreeEntry tracks a worktree Termixgo created, so prune can find one that
// has gone idle instead of leaving every parallel checkout behind forever.
type worktreeEntry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Branch     string    `json:"branch,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
}

// worktreeRegistryFile lives in the workspace state directory, which the
// workspace walk already skips, so the registry is state and not content.
func worktreeRegistryFile(workspace string) string {
	return filepath.Join(workspace, ".termixgo", "worktrees.json")
}

func loadWorktreeEntries(workspace string) ([]worktreeEntry, error) {
	data, err := os.ReadFile(worktreeRegistryFile(workspace))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []worktreeEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func saveWorktreeEntries(workspace string, entries []worktreeEntry) error {
	path := worktreeRegistryFile(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// recordWorktree adds or replaces an entry by path.
func recordWorktree(workspace string, entry worktreeEntry) error {
	entries, err := loadWorktreeEntries(workspace)
	if err != nil {
		return err
	}
	replaced := false
	for index := range entries {
		if entries[index].Path == entry.Path {
			entries[index] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, entry)
	}
	return saveWorktreeEntries(workspace, entries)
}

// forgetWorktree drops an entry by path.
func forgetWorktree(workspace, path string) error {
	entries, err := loadWorktreeEntries(workspace)
	if err != nil {
		return err
	}
	kept := make([]worktreeEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Path != path {
			kept = append(kept, entry)
		}
	}
	return saveWorktreeEntries(workspace, kept)
}

// touchWorktree updates the last-used time for a tracked path.
func touchWorktree(workspace, path string, now time.Time) error {
	entries, err := loadWorktreeEntries(workspace)
	if err != nil {
		return err
	}
	for index := range entries {
		if entries[index].Path == path {
			entries[index].LastUsedAt = now
		}
	}
	return saveWorktreeEntries(workspace, entries)
}

// snapshotName builds a valid git ref component for a worktree snapshot.
func snapshotName(name string, now time.Time) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, strings.TrimSpace(name))
	if strings.Trim(clean, "-") == "" {
		clean = "worktree"
	}
	return clean + "-" + now.UTC().Format("20060102T150405")
}

// shortAge renders a duration the way a worktree listing should read.
func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "0m"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	}
}
