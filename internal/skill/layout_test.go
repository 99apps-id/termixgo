package skill

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

// TestProjectSkillsLiveUnderTermixgo pins the on-disk layout to the app's own
// name. Termixgo was built from the Termigo repositories, so the project
// directory used to be ".termigo"; a rename that only touched some call sites
// would silently stop finding skills, so the path itself is asserted here.
func TestProjectSkillsLiveUnderTermixgo(t *testing.T) {
	workspace := t.TempDir()
	want := filepath.Join(workspace, ".termixgo", "skills")
	if got := ProjectDir(workspace); got != want {
		t.Fatalf("ProjectDir = %q, want %q", got, want)
	}
	if strings.Contains(ProjectDir(workspace), string(filepath.Separator)+".termigo"+string(filepath.Separator)) {
		t.Fatalf("ProjectDir still points at the old .termigo layout: %q", ProjectDir(workspace))
	}
}

// TestUserSkillsShareTheStateDirectory checks the two scopes agree, which is
// what lets one folder serve as both only when the workspace is the home
// directory.
func TestUserSkillsShareTheStateDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	want := filepath.Join(home, "skills")
	if got := UserDir(); got != want {
		t.Fatalf("UserDir = %q, want %q", got, want)
	}
}

// TestLegacyTermigoDirectoryIsIgnored is the regression guard: a leftover
// .termigo/skills folder from the reference repositories must not be indexed.
// Reading it would mix two projects' skills into one prompt.
func TestLegacyTermigoDirectoryIsIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	workspace := t.TempDir()

	writeSkill(t, filepath.Join(workspace, ".termixgo", "skills", "current"), "current", "Current layout.")
	writeSkill(t, filepath.Join(workspace, ".termigo", "skills", "legacy"), "legacy", "Old layout.")

	skills, err := Discover(workspace)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	names := map[string]bool{}
	for _, item := range skills {
		names[item.Name] = true
	}
	if !names["current"] {
		t.Errorf("the .termixgo skill should be found, got %v", names)
	}
	if names["legacy"] {
		t.Errorf("the legacy .termigo skill must not be indexed, got %v", names)
	}
}

// TestPromptBlockNamesTheDirectory keeps the system prompt honest: the model is
// told where skills live, so a stale path there sends it looking in the wrong
// place.
func TestPromptBlockNamesTheDirectory(t *testing.T) {
	block := PromptBlock([]Skill{{Name: "review", Description: "Review."}})
	if !strings.Contains(block, ".termixgo/skills/") {
		t.Errorf("the prompt block must name the real directory, got %q", block)
	}
	if strings.Contains(block, ".termigo/skills/") {
		t.Errorf("the prompt block still names the legacy directory, got %q", block)
	}
}
