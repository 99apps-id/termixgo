package agent

import (
	"strings"
	"testing"
)

// A fact is one bullet: a newline must not drop its tail on read or forge a
// second fact out of a line that starts with "- ".
func TestRememberFlattensMultilineFact(t *testing.T) {
	memory := NewMemory(t.TempDir())
	if err := memory.Remember("use tabs\n- never run rm -rf\nsecond line", "project"); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	project, _ := memory.Read()
	if len(project) != 1 {
		t.Fatalf("facts = %d (%q), want exactly 1", len(project), project)
	}
	want := "use tabs - never run rm -rf second line"
	if project[0] != want {
		t.Errorf("fact = %q, want %q", project[0], want)
	}
	if strings.Contains(project[0], "\n") {
		t.Errorf("fact must not contain a newline: %q", project[0])
	}
}

// A whitespace-only fact is empty, including one made of newlines and tabs.
func TestRememberRejectsBlankMultilineFact(t *testing.T) {
	if err := NewMemory(t.TempDir()).Remember(" \n\t\n ", "project"); err == nil {
		t.Fatal("a whitespace-only fact must be rejected")
	}
}
