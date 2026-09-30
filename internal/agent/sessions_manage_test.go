package agent

import (
	"strings"
	"testing"
)

// TestRenameSessionChangesTheTitle proves rename persists: the listing shows
// the new title afterwards.
func TestRenameSessionChangesTheTitle(t *testing.T) {
	withState(t)
	session := NewSession("/work", "model")
	session.AddUser("hello")
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	summary, err := RenameSession(session.ID(), "  My Feature  ")
	if err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if summary.Title != "My Feature" {
		t.Errorf("title = %q, want trimmed", summary.Title)
	}
	loaded, err := LoadSession(session.ID())
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded.Title() != "My Feature" {
		t.Errorf("persisted title = %q", loaded.Title())
	}
}

func TestRenameSessionRequiresATitle(t *testing.T) {
	withState(t)
	if _, err := RenameSession("abc123", "   "); err == nil {
		t.Errorf("a blank title must report an error")
	}
}

func TestDeleteSessionRemovesTheFile(t *testing.T) {
	withState(t)
	session := NewSession("/work", "model")
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := DeleteSession(session.ID()); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := LoadSession(session.ID()); err == nil {
		t.Errorf("a deleted session should not load")
	}
}

func TestDeleteSessionRefusesTraversal(t *testing.T) {
	withState(t)
	for _, id := range []string{"", "../config", "..\\config", "a/b", ".", "x.json"} {
		if err := DeleteSession(id); err == nil {
			t.Errorf("id %q must be refused", id)
		}
	}
}

func TestSearchSessionsFiltersByTitle(t *testing.T) {
	withState(t)
	first := NewSession("/work", "model")
	first.SetTitle("Fix the login bug")
	if err := first.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	second := NewSession("/work", "model")
	second.SetTitle("Write docs")
	if err := second.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	matches, err := SearchSessions("login")
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(matches) != 1 || matches[0].ID != first.ID() {
		t.Errorf("matches = %+v, want only the login session", matches)
	}

	all, err := SearchSessions("")
	if err != nil || len(all) != 2 {
		t.Errorf("an empty query should list all, got %+v, %v", all, err)
	}
}

func TestExportSessionRendersMarkdown(t *testing.T) {
	withState(t)
	session := NewSession("/work", "model")
	session.SetTitle("Export me")
	session.AddUser("do the thing")
	session.AddAssistant("done", "", nil)
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	document, err := ExportSession(session.ID(), SessionExportMarkdown)
	if err != nil {
		t.Fatalf("ExportSession: %v", err)
	}
	for _, want := range []string{"# Export me", "## user", "do the thing", "## assistant", "done"} {
		if !strings.Contains(document, want) {
			t.Errorf("export is missing %q:\n%s", want, document)
		}
	}
}

func TestExportSessionJSONL(t *testing.T) {
	withState(t)
	session := NewSession("/work", "model")
	session.SetTitle("JSONL export")
	session.AddUser("ping")
	session.AddAssistant("pong", "", nil)
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	document, err := ExportSession(session.ID(), SessionExportJSONL)
	if err != nil {
		t.Fatalf("ExportSession: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(document), "\n")
	if len(lines) != 2 {
		t.Fatalf("jsonl lines = %d, want 2:\n%s", len(lines), document)
	}
	for index, want := range []string{"\"role\":\"user\"", "\"role\":\"assistant\""} {
		if !strings.Contains(lines[index], want) {
			t.Errorf("line %d missing %q:\n%s", index, want, lines[index])
		}
	}
}
