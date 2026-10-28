// Package agent records errors in a journal so the loop can learn from
// repeated mistakes and suggest corrections.
package agent

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// journalEntry records one tool failure.
type journalEntry struct {
	Timestamp time.Time `json:"ts"`
	Tool      string    `json:"tool"`
	Error     string    `json:"error"`
	Args      string    `json:"args,omitempty"`
	Session   string    `json:"session,omitempty"`
}

// journalPattern is a recurring mistake the agent has seen.
type journalPattern struct {
	Tool     string `json:"tool"`
	Error    string `json:"error"`
	Count    int    `json:"count"`
	LastSeen string `json:"last_seen,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

// ErrorJournal persists tool failures so the agent can learn from them.
type ErrorJournal struct {
	mu    sync.Mutex
	path  string
	limit int
}

// NewErrorJournal opens the journal file, creating it when missing.
func NewErrorJournal(workspace string) (*ErrorJournal, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, fmt.Errorf("journal: workspace is required")
	}
	path := filepath.Join(workspace, ".termixgo", "error-journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return &ErrorJournal{path: path, limit: 2000}, nil
}

// journalArgLimit clips the recorded arguments of one failure. Arguments are
// model-authored and a write or patch can carry hundreds of kilobytes; without
// a clip one oversized entry made the reader's line cap permanent: the
// journal stopped being readable, the trim never fired again and the file
// grew with every failed call for the rest of the session.
const journalArgLimit = 4096

// Path reports the journal file, so the full-text index can index it as one
// document without duplicating the naming convention here.
func (j *ErrorJournal) Path() string {
	if j == nil {
		return ""
	}
	return j.path
}

// Record appends one failure. It writes synchronously so the caller can
// observe the entry immediately. The file is small and the write is fast,
// so the agent loop is not meaningfully slowed down.
func (j *ErrorJournal) Record(tool, args, errStr string) {
	if j == nil {
		return
	}
	entry := journalEntry{
		Timestamp: time.Now().UTC(),
		Tool:      tool,
		Error:     clipBytes(errStr, journalArgLimit),
		Args:      clipBytes(args, journalArgLimit),
	}
	_ = j.append(entry)
}

// Patterns returns the most frequent recurring mistakes.
func (j *ErrorJournal) Patterns() []journalPattern {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	entries, err := j.readAllLocked()
	if err != nil {
		return nil
	}
	// collapse similar errors
	type key struct{ tool, error string }
	counts := make(map[key]*journalPattern)
	for _, e := range entries {
		k := key{e.Tool, normalizeError(e.Error)}
		if p, ok := counts[k]; ok {
			p.Count++
			p.LastSeen = e.Timestamp.Format(time.RFC3339)
		} else {
			counts[k] = &journalPattern{
				Tool:     e.Tool,
				Error:    normalizeError(e.Error),
				Count:    1,
				LastSeen: e.Timestamp.Format(time.RFC3339),
				Hint:     suggestFix(e.Tool, e.Error),
			}
		}
	}
	var out []journalPattern
	for _, p := range counts {
		if p.Count >= 2 {
			out = append(out, *p)
		}
	}
	// sort by count desc, then tool asc
	sort.Slice(out, func(i, k int) bool {
		if out[k].Count != out[i].Count {
			return out[i].Count > out[k].Count
		}
		return out[i].Tool < out[k].Tool
	})
	return out
}

// SelfCorrectionHints returns short advice the agent should apply before
// retrying a failing tool.
func (j *ErrorJournal) SelfCorrectionHints(tool string) []string {
	if j == nil {
		return nil
	}
	patterns := j.Patterns()
	var hints []string
	for _, p := range patterns {
		if p.Tool == tool && p.Count >= 2 {
			hints = append(hints, p.Hint)
		}
	}
	return hints
}

// append writes one entry to the JSONL file, trimming when over the limit.
func (j *ErrorJournal) append(entry journalEntry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(entry)
	if _, err := f.Write(appendNewline(data)); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// trim if needed
	entries, readErr := j.readAllLocked()
	if readErr == nil && len(entries) > j.limit {
		trim := entries[len(entries)-j.limit:]
		f2, err := os.CreateTemp(filepath.Dir(j.path), "journal-*.tmp")
		if err != nil {
			return err
		}
		name := f2.Name()
		w := bufio.NewWriter(f2)
		var writeErr error
		for _, e := range trim {
			data, _ := json.Marshal(e)
			if _, writeErr = w.Write(appendNewline(data)); writeErr != nil {
				break
			}
		}
		if writeErr == nil {
			writeErr = w.Flush()
		}
		if closeErr := f2.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			_ = os.Remove(name)
			return writeErr
		}
		if err := os.Rename(name, j.path); err != nil {
			_ = os.Remove(name)
			return err
		}
	}
	return nil
}

// readAllLocked assumes j.mu is held.
//
// A plain reader loop is used rather than Scanner: Scanner stops for good on
// one line past its cap, which left the journal permanently unreadable after
// a single oversized legacy entry. This loop drains an oversized line and
// carries on, and the clip in Record keeps new entries short.
func (j *ErrorJournal) readAllLocked() ([]journalEntry, error) {
	f, err := os.Open(j.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var entries []journalEntry
	reader := bufio.NewReaderSize(f, 64*1024)
	overflow := false
	for {
		line, readErr := reader.ReadSlice('\n')
		if readErr == bufio.ErrBufferFull {
			// Line longer than the buffer: drain the rest of it and skip.
			overflow = true
			for readErr == bufio.ErrBufferFull {
				_, readErr = reader.ReadSlice('\n')
			}
			continue
		}
		if overflow {
			overflow = false
			if readErr != nil {
				break
			}
			continue
		}
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			var e journalEntry
			if json.Unmarshal(line, &e) == nil {
				entries = append(entries, e)
			}
		}
		if readErr != nil {
			break
		}
	}
	return entries, nil
}

// normalizeError collapses an error message to a canonical form for grouping.
var pathRe = regexp.MustCompile(`[A-Za-z]:\\[^\s]*|/[^\s]*`)

func normalizeError(err string) string {
	err = pathRe.ReplaceAllString(err, "<path>")
	err = strings.ReplaceAll(err, " %!s(<nil>)", "")
	err = strings.TrimSpace(err)
	if err == "" {
		return "(empty error)"
	}
	// collapse repeated runs
	var b strings.Builder
	prev := byte(0)
	count := 0
	for i := 0; i < len(err); i++ {
		c := err[i]
		if c == prev && (c == ' ' || c == '\t' || c == '\n') {
			count++
			if count > 2 {
				continue
			}
		} else {
			count = 0
		}
		prev = c
		b.WriteByte(c)
	}
	return b.String()
}

// suggestFix returns a short hint for a known failure mode.
func suggestFix(tool, err string) string {
	lower := strings.ToLower(err)
	switch {
	case strings.Contains(lower, "no workspace configured"):
		return "ensure env.Workspace is set before running filesystem tools"
	case strings.Contains(lower, "not a git repository"):
		return "run git init in the workspace or choose a different directory"
	case strings.Contains(lower, "path escapes the workspace"):
		return "use paths inside the workspace; ../ traversal is blocked"
	case strings.Contains(lower, "no api key"):
		return "run /setup to add the provider key"
	case strings.Contains(lower, "context canceled"):
		return "the turn was stopped; wait for the operator to retry"
	case strings.Contains(lower, "executable file not found"):
		return "check the command name and PATH"
	case strings.Contains(lower, "permission denied"):
		return "check file permissions or run with elevated rights"
	case strings.Contains(lower, "no model is configured"):
		return "run /setup to choose a default model"
	case strings.Contains(lower, "old_string") && (strings.Contains(lower, "not found") || strings.Contains(lower, "match")):
		return "read the file fresh before editing; old_string must match byte-for-byte including exact whitespace"
	case strings.Contains(lower, "ambiguous") || strings.Contains(lower, "multiple matches"):
		return "provide more surrounding context lines in old_string or set replace_all=true"
	case strings.Contains(lower, "command timed out") || strings.Contains(lower, "timed out"):
		return "increase timeout_secs or check if the process is waiting for interactive input"
	case strings.Contains(lower, "bad revision"):
		return "use a valid git commit sha, branch name, or HEAD without option flags"
	case strings.Contains(lower, "rate limited"):
		return "back off and retry after the suggested delay"
	case strings.Contains(lower, "connection refused"), strings.Contains(lower, "no such host"):
		return "check network connectivity and the provider base URL"
	}
	return ""
}

// Compare journalPatterns for sorting.
func (p journalPattern) Compare(o journalPattern) int {
	if p.Count != o.Count {
		return cmp.Compare(o.Count, p.Count)
	}
	return cmp.Compare(p.Tool, o.Tool)
}

// appendNewline returns a copy of data with a trailing newline, allocated on a
// fresh buffer so json.Marshal output is never overwritten past its length.
func appendNewline(data []byte) []byte {
	out := make([]byte, len(data)+1)
	copy(out, data)
	out[len(data)] = '\n'
	return out
}
