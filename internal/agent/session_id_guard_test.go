package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadSessionRefusesAnEscapingID covers the asymmetry between the two
// session entry points.
//
// DeleteSession confines an id with checkSessionID, so a separator is refused
// rather than joined. LoadSession joined the id straight into the sessions
// directory, so an id such as "../name" read a file one level above it. The id
// is operator input from /sessions, which is exactly the kind of value a guard
// belongs on.
func TestLoadSessionRefusesAnEscapingID(t *testing.T) {
	home := withState(t)

	// A well-formed session file one level above the sessions directory, which
	// is what a traversal would reach.
	outside := sessionJSON{ID: "escape", Workspace: "/w", Model: "m"}
	data, err := json.Marshal(outside)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "escape.json"), data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if session, err := LoadSession("../escape"); err == nil {
		t.Fatalf("LoadSession accepted an escaping id and loaded %q", session.ID())
	}

	// A plain id must still resolve, so the guard is not a blanket refusal.
	inside := NewSession("/w", "m")
	inside.AddUser("kept")
	if err := inside.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := LoadSession(inside.ID()); err != nil {
		t.Errorf("a normal id should still load: %v", err)
	}
}
