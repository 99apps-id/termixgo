package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// readAll returns all entries from the journal file. Tests only.
func (j *ErrorJournal) readAll() ([]journalEntry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.readAllLocked()
}

func TestErrorJournalRecordsAndPatterns(t *testing.T) {
	workspace := t.TempDir()
	journal, err := NewErrorJournal(workspace)
	if err != nil {
		t.Fatalf("NewErrorJournal: %v", err)
	}

	journal.Record("read_file", `{"path":"/etc/passwd"}`, "path escapes the workspace")
	journal.Record("read_file", `{"path":"/etc/shadow"}`, "path escapes the workspace")
	journal.Record("git_status", `{"path":"."}`, "not a git repository")

	// Patterns returns only recurring mistakes (count >= 2).
	patterns := journal.Patterns()
	if len(patterns) != 1 {
		t.Fatalf("patterns = %d, want 1 (recurring only)", len(patterns))
	}

	readFilePattern := patterns[0]
	if readFilePattern.Tool != "read_file" {
		t.Errorf("first pattern tool = %q, want read_file", readFilePattern.Tool)
	}
	if readFilePattern.Count != 2 {
		t.Errorf("read_file count = %d, want 2", readFilePattern.Count)
	}
	if readFilePattern.Hint == "" {
		t.Errorf("read_file hint must not be empty")
	}
}

func TestErrorJournalSelfCorrectionHints(t *testing.T) {
	workspace := t.TempDir()
	journal, err := NewErrorJournal(workspace)
	if err != nil {
		t.Fatalf("NewErrorJournal: %v", err)
	}

	journal.Record("run_command", `{"command":"rm -rf /"}`, "permission denied")
	journal.Record("run_command", `{"command":"rm -rf /"}`, "permission denied")

	hints := journal.SelfCorrectionHints("run_command")
	if len(hints) == 0 {
		t.Errorf("expected hints for run_command, got none")
	}
}

func TestErrorJournalNilIsSafe(t *testing.T) {
	var journal *ErrorJournal
	journal.Record("tool", "{}", "error")
	patterns := journal.Patterns()
	if len(patterns) != 0 {
		t.Errorf("nil journal patterns = %d, want 0", len(patterns))
	}
	hints := journal.SelfCorrectionHints("tool")
	if len(hints) != 0 {
		t.Errorf("nil journal hints = %d, want 0", len(hints))
	}
}

func TestNormalizeErrorCollapsesPaths(t *testing.T) {
	input := `open /home/user/file.go: permission denied`
	normalized := normalizeError(input)
	if strings.Contains(normalized, "/home/user/file.go") {
		t.Errorf("normalized error must not contain raw path: %s", normalized)
	}
	if !strings.Contains(normalized, "<path>") {
		t.Errorf("normalized error must contain <path>: %s", normalized)
	}
}

func TestSuggestFixReturnsHints(t *testing.T) {
	tests := []struct {
		tool, err, want string
	}{
		{"read_file", "path escapes the workspace", "use paths inside the workspace"},
		{"run_command", "permission denied", "check file permissions"},
		{"git_status", "not a git repository", "run git init"},
		{"unknown", "something weird", ""},
	}
	for _, tc := range tests {
		got := suggestFix(tc.tool, tc.err)
		if tc.want == "" {
			if got != "" {
				t.Errorf("suggestFix(%q, %q) = %q, want empty", tc.tool, tc.err, got)
			}
		} else {
			if !strings.Contains(got, tc.want) {
				t.Errorf("suggestFix(%q, %q) = %q, want substring %q", tc.tool, tc.err, got, tc.want)
			}
		}
	}
}

func TestErrorJournalPersistsAcrossInstances(t *testing.T) {
	workspace := t.TempDir()
	journal1, err := NewErrorJournal(workspace)
	if err != nil {
		t.Fatalf("NewErrorJournal: %v", err)
	}
	journal1.Record("edit", `{"path":"x.go"}`, "file not found")

	journal2, err := NewErrorJournal(workspace)
	if err != nil {
		t.Fatalf("NewErrorJournal: %v", err)
	}
	// Patterns filters to count >= 2, so a single entry returns no patterns.
	patterns := journal2.Patterns()
	if len(patterns) != 0 {
		t.Fatalf("patterns = %d, want 0 for a single entry", len(patterns))
	}
	// Verify the entry was persisted by reading all entries.
	entries, err := journal2.readAll()
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Tool != "edit" {
		t.Errorf("tool = %q, want edit", entries[0].Tool)
	}
}

func TestErrorJournalTrimsOldEntries(t *testing.T) {
	workspace := t.TempDir()
	journal, err := NewErrorJournal(workspace)
	if err != nil {
		t.Fatalf("NewErrorJournal: %v", err)
	}
	journal.limit = 5
	for i := 0; i < 10; i++ {
		journal.Record("tool", "{}", "error")
	}
	entries, _ := journal.readAll()
	if len(entries) > 5 {
		t.Errorf("entries = %d, want <= 5", len(entries))
	}
}

func TestErrorJournalFilePermissions(t *testing.T) {
	workspace := t.TempDir()
	journal, err := NewErrorJournal(workspace)
	if err != nil {
		t.Fatalf("NewErrorJournal: %v", err)
	}
	// Create the file by recording an entry.
	journal.Record("tool", "{}", "error")
	info, err := os.Stat(journal.path)
	if err != nil {
		t.Fatalf("stat journal: %v", err)
	}
	// On Windows, Go's permission bits are best-effort and often resolve to
	// 0666 for files created by the current user. The important thing is that
	// the file exists and is owned by the current user.
	mode := info.Mode().Perm()
	if mode&0o077 != 0 && runtime.GOOS != "windows" {
		t.Errorf("journal permissions too open: %o", mode)
	}
}

func TestErrorJournalHandlesMalformedLines(t *testing.T) {
	workspace := t.TempDir()
	journal, err := NewErrorJournal(workspace)
	if err != nil {
		t.Fatalf("NewErrorJournal: %v", err)
	}
	_ = os.WriteFile(journal.path, []byte("not json\n{}\n{bad}\n"), 0o600)
	entries, err := journal.readAll()
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("entries = %d, want 1 (only valid JSON line)", len(entries))
	}
}

func TestErrorJournalDirectoryCreation(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "deep", "workspace")
	_, err := NewErrorJournal(workspace)
	if err != nil {
		t.Fatalf("NewErrorJournal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".termixgo")); err != nil {
		t.Fatalf(".termixgo dir must be created: %v", err)
	}
}
