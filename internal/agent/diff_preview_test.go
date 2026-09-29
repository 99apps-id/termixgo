package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func previewEnv(t *testing.T, files map[string]string) *Env {
	t.Helper()
	workspace := t.TempDir()
	for name, content := range files {
		path := filepath.Join(workspace, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true}
}

func TestPreviewEditShowsChangedLines(t *testing.T) {
	env := previewEnv(t, map[string]string{"main.go": "package main\n\nfunc A() {}\n"})
	got := PreviewToolDiff(env, "edit", map[string]any{
		"path": "main.go", "old_string": "func A() {}", "new_string": "func A() int {}",
	})
	for _, want := range []string{"-func A() {}", "+func A() int {}", "package main"} {
		if !strings.Contains(got, want) {
			t.Errorf("preview is missing %q:\n%s", want, got)
		}
	}
}

func TestPreviewEditMissesGracefully(t *testing.T) {
	env := previewEnv(t, map[string]string{"main.go": "package main\n"})
	if got := PreviewToolDiff(env, "edit", map[string]any{"path": "main.go", "old_string": "absent", "new_string": "x"}); got != "" {
		t.Errorf("an unmatched edit should yield no preview, got %q", got)
	}
	if got := PreviewToolDiff(env, "run_command", map[string]any{"command": "ls"}); got != "" {
		t.Errorf("non-file tools should yield no preview, got %q", got)
	}
}

func TestPreviewWriteNewFileSummarises(t *testing.T) {
	env := previewEnv(t, nil)
	got := PreviewToolDiff(env, "write_file", map[string]any{"path": "new.go", "content": "package new\n"})
	if !strings.Contains(got, "new file") {
		t.Errorf("a new file should be summarised:\n%s", got)
	}
}

func TestPreviewWriteExistingDiffs(t *testing.T) {
	env := previewEnv(t, map[string]string{"a.txt": "old\n"})
	got := PreviewToolDiff(env, "write_file", map[string]any{"path": "a.txt", "content": "new\n"})
	if !strings.Contains(got, "-old") || !strings.Contains(got, "+new") {
		t.Errorf("an overwrite should diff:\n%s", got)
	}
}

func TestPreviewPatchShowsHunks(t *testing.T) {
	env := previewEnv(t, map[string]string{"a.go": "package a\n\nfunc A() {}\n"})
	document := "*** Begin Patch\n*** Update File: a.go\n@@\n-func A() {}\n+func A() int {}\n*** End Patch\n"
	got := PreviewToolDiff(env, "apply_patch", map[string]any{"patch": document})
	if !strings.Contains(got, "-func A() {}") || !strings.Contains(got, "+func A() int {}") {
		t.Errorf("a patch should preview:\n%s", got)
	}
}

func TestPreviewRefusesOutsidePaths(t *testing.T) {
	env := previewEnv(t, nil)
	if got := PreviewToolDiff(env, "edit", map[string]any{"path": "../escape", "old_string": "a", "new_string": "b"}); got != "" {
		t.Errorf("outside paths should yield no preview, got %q", got)
	}
}
