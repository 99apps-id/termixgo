package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

func TestRunSessionsCLI(t *testing.T) {
	// Set isolated state dir
	home := t.TempDir()
	t.Setenv("TERMIXGO_HOME", home)

	var buf bytes.Buffer

	// 1. List when empty
	buf.Reset()
	if err := runSessions([]string{"list"}, &buf); err != nil {
		t.Fatalf("runSessions list: %v", err)
	}
	if !strings.Contains(buf.String(), "No saved sessions yet") {
		t.Errorf("expected empty sessions message, got: %s", buf.String())
	}

	// 2. Create a dummy session
	session := agent.NewSession("test-model", "test-workspace")
	session.SetTitle("Feature Implementation")
	session.AddUser("Please implement this")
	session.AddAssistant("Here is the solution", "", nil)
	if err := session.Save(); err != nil {
		t.Fatalf("save session: %v", err)
	}
	id := session.ID()

	// 3. List with session
	buf.Reset()
	if err := runSessions([]string{"list"}, &buf); err != nil {
		t.Fatalf("runSessions list: %v", err)
	}
	if !strings.Contains(buf.String(), "Saved sessions (1)") || !strings.Contains(buf.String(), id) {
		t.Errorf("expected session in list output, got: %s", buf.String())
	}

	// 4. Search session
	buf.Reset()
	if err := runSessions([]string{"search", "Feature"}, &buf); err != nil {
		t.Fatalf("runSessions search: %v", err)
	}
	if !strings.Contains(buf.String(), "Matching sessions (1)") || !strings.Contains(buf.String(), "Feature Implementation") {
		t.Errorf("expected matching session in search output, got: %s", buf.String())
	}

	// 5. Export session
	buf.Reset()
	if err := runSessions([]string{"export", id}, &buf); err != nil {
		t.Fatalf("runSessions export: %v", err)
	}
	if !strings.Contains(buf.String(), "# Feature Implementation") || !strings.Contains(buf.String(), "Please implement this") {
		t.Errorf("expected markdown export, got: %s", buf.String())
	}

	// 6. Rename session
	buf.Reset()
	if err := runSessions([]string{"rename", id, "New Title Here"}, &buf); err != nil {
		t.Fatalf("runSessions rename: %v", err)
	}
	if !strings.Contains(buf.String(), "Renamed session") || !strings.Contains(buf.String(), "New Title Here") {
		t.Errorf("expected rename output, got: %s", buf.String())
	}

	// 7. Delete session
	buf.Reset()
	if err := runSessions([]string{"delete", id}, &buf); err != nil {
		t.Fatalf("runSessions delete: %v", err)
	}
	if !strings.Contains(buf.String(), "Deleted session") {
		t.Errorf("expected delete output, got: %s", buf.String())
	}
}
