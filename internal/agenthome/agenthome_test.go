package agenthome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestEnsureCreatesHomeAndPersona(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	paths, err := Ensure("sales", "", "sales")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, dir := range []string{paths.Home, paths.Workspace, paths.Sessions, paths.Memories, paths.Skills} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("directory %s was not created: %v", dir, err)
		}
	}
	soul, err := os.ReadFile(paths.Soul)
	if err != nil {
		t.Fatalf("read SOUL.md: %v", err)
	}
	if !strings.Contains(string(soul), "asisten penjualan") {
		t.Errorf("the sales template was not written: %q", string(soul))
	}
	if _, err := os.Stat(paths.User); err != nil {
		t.Errorf("USER.md was not written: %v", err)
	}
}

func TestEnsureKeepsExistingPersona(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	paths, err := Ensure("sales", "", "sales")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	custom := "# SOUL.md\n\n## Persona\nMilik saya sendiri.\n"
	if err := os.WriteFile(paths.Soul, []byte(custom), 0o600); err != nil {
		t.Fatalf("write SOUL.md: %v", err)
	}
	if _, err := Ensure("sales", "", "support"); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	content, err := os.ReadFile(paths.Soul)
	if err != nil {
		t.Fatalf("read SOUL.md: %v", err)
	}
	if string(content) != custom {
		t.Errorf("Ensure overwrote the operator's persona: %q", string(content))
	}
}

func TestEnsureRejectsBadNamesAndTemplates(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if _, err := Ensure("Sales Bot", "", "sales"); err == nil {
		t.Errorf("an invalid name was accepted")
	}
	if _, err := Ensure("sales", "", "nope"); err == nil {
		t.Errorf("an unknown template was accepted")
	}
}

func TestSessionsDirForKeepsDefaultShared(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	shared, err := config.SessionsDir()
	if err != nil {
		t.Fatalf("SessionsDir: %v", err)
	}
	got, err := SessionsDirFor(config.FinalAgentName)
	if err != nil {
		t.Fatalf("SessionsDirFor default: %v", err)
	}
	if got != shared {
		t.Errorf("the default agent must keep the shared sessions directory: %q vs %q", got, shared)
	}
	named, err := SessionsDirFor("sales")
	if err != nil {
		t.Fatalf("SessionsDirFor sales: %v", err)
	}
	paths, err := Resolve("sales", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if named != paths.Sessions {
		t.Errorf("a named agent's sessions must live in its home: %q vs %q", named, paths.Sessions)
	}
	if !strings.HasPrefix(named, paths.Home+string(filepath.Separator)) {
		t.Errorf("the sessions directory escaped the agent home: %q", named)
	}
}
