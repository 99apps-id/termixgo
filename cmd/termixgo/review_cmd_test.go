package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initCliGitRepo(t *testing.T, dir string) {
	t.Helper()
	runCmd := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v, output: %s", strings.Join(args, " "), err, string(out))
		}
	}

	runCmd("init")
	runCmd("config", "user.email", "test@example.com")
	runCmd("config", "user.name", "Test User")
}

func TestRunReviewCommand(t *testing.T) {
	dir := t.TempDir()
	initCliGitRepo(t, dir)

	originalWd, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(originalWd) }()

	docFile := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(docFile, []byte("line 1\n"), 0o644)

	cmd := exec.Command("git", "add", "notes.txt")
	cmd.Dir = dir
	_ = cmd.Run()

	cmd = exec.Command("git", "commit", "-m", "init")
	cmd.Dir = dir
	_ = cmd.Run()

	_ = os.WriteFile(docFile, []byte("line 1\nline 2 added\n"), 0o644)

	var stdout bytes.Buffer
	err := runReview([]string{"--static"}, &stdout)
	if err != nil {
		t.Fatalf("runReview error: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Git Diff Review Report") || !strings.Contains(out, "notes.txt") {
		t.Errorf("expected review report for notes.txt, got:\n%s", out)
	}

	// JSON format
	var jsonStdout bytes.Buffer
	err = runReview([]string{"--static", "--json"}, &jsonStdout)
	if err != nil {
		t.Fatalf("runReview --json error: %v", err)
	}

	jsonOut := jsonStdout.String()
	if !strings.Contains(jsonOut, `"files_changed":`) || !strings.Contains(jsonOut, "notes.txt") {
		t.Errorf("expected json output for review, got:\n%s", jsonOut)
	}
}
