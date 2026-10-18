package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// TestEphemeralRunWritesNoSessionFile is the CI guard. `termixgo run` is a
// command, so a hundred invocations must not leave a hundred session files.
func TestEphemeralRunWritesNoSessionFile(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()

	application.SetEphemeral(true)
	if !application.Ephemeral() {
		t.Fatalf("Ephemeral should be true after SetEphemeral(true)")
	}
	if err := application.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	dir, err := config.SessionsDir()
	if err != nil {
		t.Fatalf("SessionsDir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("an ephemeral run left session files behind: %v", names)
	}
}

// TestNormalRunStillWritesASessionFile proves the ephemeral flag is the only
// thing suppressing persistence, rather than persistence being broken.
func TestNormalRunStillWritesASessionFile(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()

	if application.Ephemeral() {
		t.Fatalf("a fresh app should persist by default")
	}
	if err := application.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	dir, err := config.SessionsDir()
	if err != nil {
		t.Fatalf("SessionsDir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected exactly one session file, got %d", len(entries))
	}
}

// TestSessionKeepsItsTodosAndCostAcrossASave checks the fields a resumed
// session needs, so a restart does not reset the plan or the spend.
func TestSessionKeepsItsTodosAndCostAcrossASave(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()

	application.SetEphemeral(false)
	application.Todos().Set([]agent.Todo{{ID: "t1", Title: "ship it", Status: "in_progress"}})
	if err := application.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	session := application.Session()
	todos := session.Todos()
	if len(todos) != 1 || todos[0].Title != "ship it" {
		t.Errorf("todos were not persisted: %+v", todos)
	}

	reloaded, err := agent.LoadSession(session.ID())
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(reloaded.Todos()) != 1 {
		t.Errorf("reloaded todos = %+v", reloaded.Todos())
	}
	if filepath.Base(reloaded.Workspace()) == "" {
		t.Errorf("the workspace should survive a round trip")
	}
}
