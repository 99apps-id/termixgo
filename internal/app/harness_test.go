package app

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// TestHarnessProfileDefaultsToCritical pins the full-agentic default at the app
// layer too: an unset profile must resolve rather than leave the turn without a
// harness.
func TestHarnessProfileDefaultsToCritical(t *testing.T) {
	application := newTestApp(t)

	active := application.HarnessProfile()
	if active.ID != agent.DefaultHarnessProfile {
		t.Errorf("profile = %q, want the default %q", active.ID, agent.DefaultHarnessProfile)
	}
	if strings.TrimSpace(active.PromptPrelude) == "" {
		t.Errorf("the default profile should carry guidance")
	}
}

// TestSetHarnessProfilePersistsAndRejects is the command contract: a known id
// sticks, and an unknown one is refused with the alternatives named.
func TestSetHarnessProfilePersistsAndRejects(t *testing.T) {
	application := newTestApp(t)

	profile, err := application.SetHarnessProfile("autonomous")
	if err != nil {
		t.Fatalf("SetHarnessProfile: %v", err)
	}
	if profile.ID != "autonomous" {
		t.Errorf("profile = %q", profile.ID)
	}
	if got := application.HarnessProfile().ID; got != "autonomous" {
		t.Errorf("the app still reports %q after the change", got)
	}

	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.HarnessProfile != "autonomous" {
		t.Errorf("HarnessProfile = %q, want the saved id", loaded.HarnessProfile)
	}

	_, err = application.SetHarnessProfile("nonsense")
	if err == nil {
		t.Fatalf("an unknown harness must be refused")
	}
	if !strings.Contains(err.Error(), "critical") {
		t.Errorf("the error should name the alternatives, got %v", err)
	}
	// A refused change must not have written anything.
	if got := application.HarnessProfile().ID; got != "autonomous" {
		t.Errorf("a refused change altered the profile to %q", got)
	}
}
