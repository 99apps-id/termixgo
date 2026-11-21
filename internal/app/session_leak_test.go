package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

// A launch starts a fresh conversation. The newest saved one used to be loaded
// silently, so the model read the old history and resumed its pending work
// while the screen showed a new session.
func TestLaunchStartsAFreshSession(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	workspace := t.TempDir()

	first, err := newApp(t, workspace)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	old := first.Session()
	old.AddUser("finish the migration of project A")
	old.AddAssistant("Working on step 3 of 5.", "", nil, nil)
	if err := old.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	first.Shutdown()

	second, err := newApp(t, workspace)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := second.Session().MessageCount(); got != 0 {
		t.Fatalf("a new launch should start empty, got %d messages", got)
	}
	if second.Session().ID() == old.ID() {
		t.Fatalf("a new launch must not reuse the saved session")
	}
}

// /new drops steers typed for the old conversation.
func TestNewSessionDropsPendingSteers(t *testing.T) {
	application := newTestApp(t)
	application.Steer("keep going with the old task")
	application.NewSession()
	if got := application.SteerCount(); got != 0 {
		t.Fatalf("steers should be cleared, got %d", got)
	}
}
