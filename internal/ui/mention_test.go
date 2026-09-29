package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mentionWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	workspace := t.TempDir()
	for name, content := range files {
		path := filepath.Join(workspace, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return workspace
}

func TestParseMentionFindsTheToken(t *testing.T) {
	cases := []struct {
		input string
		token string
		at    int
		ok    bool
	}{
		{"fix @main", "main", 4, true},
		{"@", "", 0, true},
		{"look at @src/app", "src/app", 8, true},
		{"no mention here", "", 0, false},
		{"mail me at a@b.com", "", 0, false},
		{"done @main ", "", 0, false},
		{"@one @two", "two", 5, true},
	}
	for _, testCase := range cases {
		token, at, ok := parseMention(testCase.input)
		if token != testCase.token || at != testCase.at || ok != testCase.ok {
			t.Errorf("parseMention(%q) = (%q, %d, %v), want (%q, %d, %v)",
				testCase.input, token, at, ok, testCase.token, testCase.at, testCase.ok)
		}
	}
}

func TestMentionCandidatesPreferPrefix(t *testing.T) {
	workspace := mentionWorkspace(t, map[string]string{
		"main.go":        "package main",
		"cmd/main.go":    "package main",
		"README.md":      "docs",
		".git/config":    "git",
		"node_modules/x": "dep",
	})
	matches := mentionCandidates(workspace, "main", 10)
	if len(matches) != 2 {
		t.Fatalf("matches = %v, want the two mains", matches)
	}
	for _, match := range matches {
		if !strings.Contains(match, "main.go") {
			t.Errorf("match = %q", match)
		}
	}
	for _, match := range mentionCandidates(workspace, "", 10) {
		if strings.HasPrefix(match, ".git") || strings.HasPrefix(match, "node_modules") {
			t.Errorf("excluded dirs must not be offered: %q", match)
		}
	}
}

func TestApplyMentionCompletionReplacesTheToken(t *testing.T) {
	got := applyMentionCompletion("fix @mai", "main.go", 4)
	if got != "fix @main.go " {
		t.Errorf("completed = %q", got)
	}
}

func TestExpandMentionsInlinesFileContents(t *testing.T) {
	workspace := mentionWorkspace(t, map[string]string{"main.go": "package main\n"})
	expanded := ExpandMentions(workspace, "fix @main.go please")
	if !strings.Contains(expanded, "fix @main.go please") {
		t.Errorf("the original text should stay:\n%s", expanded)
	}
	if !strings.Contains(expanded, "--- @main.go ---") || !strings.Contains(expanded, "package main") {
		t.Errorf("the file should be inlined:\n%s", expanded)
	}
}

func TestExpandMentionsLeavesUnknownAndOutsidePaths(t *testing.T) {
	workspace := mentionWorkspace(t, map[string]string{"main.go": "x"})
	for _, input := range []string{"fix @missing.go", "read @../secret", "hello"} {
		if got := ExpandMentions(workspace, input); got != input {
			t.Errorf("ExpandMentions(%q) = %q, want unchanged", input, got)
		}
	}
}

func TestMentionMenuCompletesOnTab(t *testing.T) {
	model := chatModel(t)
	workspace := model.app.Workspace()
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	model.composer.SetValue("fix @mai")
	model.updateSlashMatches()
	if len(model.mentionMatches) == 0 {
		t.Fatalf("the menu should offer a file")
	}
	completed := press(t, model, "tab")
	if got := completed.composer.Value(); !strings.Contains(got, "@main.go") {
		t.Errorf("Tab should complete the file, got %q", got)
	}
	if view := display(completed); !strings.Contains(view, "@main.go") {
		t.Errorf("the completed text should render:\n%s", view)
	}
}
