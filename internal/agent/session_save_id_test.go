package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSaveRefusesAnEscapingIDFromTheFile closes the other half of the session
// id guard.
//
// LoadSession confines the id it is asked for, but the live session took its id
// from the file's own contents, and Save joined that id into the sessions
// directory. A stored session therefore decided where the next auto-save wrote,
// and the first turn after loading one put the transcript outside the directory
// that holds it.
func TestSaveRefusesAnEscapingIDFromTheFile(t *testing.T) {
	home := withState(t)
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(sessionJSON{ID: "../../pwned", Workspace: "/w", Model: "m"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stored.json"), data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	session, err := LoadSession("stored")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	// The id that reaches disk is the one that was validated, not the one the
	// file chose to carry.
	if got := session.ID(); got != "stored" {
		t.Errorf("session id = %q, want the requested id", got)
	}
	session.AddUser("a normal turn")
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	escape := filepath.Join(filepath.Dir(home), "pwned.json")
	if _, err := os.Stat(escape); err == nil {
		t.Errorf("a save wrote outside the sessions directory: %s", escape)
		_ = os.Remove(escape)
	}
	if _, err := os.Stat(filepath.Join(dir, "stored.json")); err != nil {
		t.Errorf("the loaded session should still update its own file: %v", err)
	}
}

// TestSaveRefusesAHostileIDDirectly is the same guard on the write side, for a
// session built in-process with an id that was never validated.
func TestSaveRefusesAHostileIDDirectly(t *testing.T) {
	withState(t)
	session := NewSession("/w", "m")
	session.adoptID("../../pwned")
	if err := session.Save(); err == nil {
		t.Fatalf("Save accepted an id LoadSession would refuse")
	}
}
