package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCommand(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestDiscoverFindsProjectCommands(t *testing.T) {
	workspace := t.TempDir()
	writeCommand(t, ProjectDir(workspace), "review.md", "---\ndescription: Review the diff\n---\nReview $ARGUMENTS carefully.")
	writeCommand(t, ProjectDir(workspace), "notes.txt", "not a command")

	commands, err := Discover(workspace)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(commands))
	}
	if commands[0].Name != "review" || commands[0].Scope != "project" {
		t.Errorf("command = %+v, want review/project", commands[0])
	}
	if commands[0].Description != "Review the diff" {
		t.Errorf("description = %q", commands[0].Description)
	}
}

func TestDiscoverWithoutFrontmatterUsesFileName(t *testing.T) {
	workspace := t.TempDir()
	writeCommand(t, ProjectDir(workspace), "deploy.md", "Deploy it now.")

	command, err := Load(workspace, "deploy")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if command.Description != "Custom command deploy" {
		t.Errorf("description = %q", command.Description)
	}
	if command.Body != "Deploy it now." {
		t.Errorf("body = %q", command.Body)
	}
}

func TestLoadIsCaseInsensitive(t *testing.T) {
	workspace := t.TempDir()
	writeCommand(t, ProjectDir(workspace), "review.md", "body")

	if _, err := Load(workspace, "REVIEW"); err != nil {
		t.Errorf("Load should ignore case: %v", err)
	}
	if _, err := Load(workspace, "missing"); err == nil {
		t.Errorf("an unknown command must report an error")
	}
}

func TestExpandPlacesArguments(t *testing.T) {
	withPlaceholder := Command{Name: "review", Body: "Review $ARGUMENTS now."}
	if got := withPlaceholder.Expand("the diff"); got != "Review the diff now." {
		t.Errorf("expand = %q", got)
	}
	if got := withPlaceholder.Expand(""); got != "Review  now." {
		t.Errorf("empty args should clear the placeholder, got %q", got)
	}
	withoutPlaceholder := Command{Name: "deploy", Body: "Deploy it."}
	if got := withoutPlaceholder.Expand("tonight"); got != "Deploy it.\n\ntonight" {
		t.Errorf("expand = %q", got)
	}
	if got := withoutPlaceholder.Expand(""); got != "Deploy it." {
		t.Errorf("expand = %q", got)
	}
}

func TestDiscoverEmptyWorkspaceHasNone(t *testing.T) {
	commands, err := Discover(t.TempDir())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(commands) != 0 {
		t.Errorf("commands = %d, want none", len(commands))
	}
}

func TestNormalizeNameKeepsTriggerCharacters(t *testing.T) {
	if got := NormalizeName("My Review!"); got != "myreview" {
		t.Errorf("normalize = %q", got)
	}
	if !strings.Contains("abc-123_x", NormalizeName("abc-123_x")) {
		t.Errorf("dashes and underscores should survive")
	}
}
