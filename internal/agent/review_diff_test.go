package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initTestGitRepo(t *testing.T, dir string) {
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

func TestReviewDiff_StaticChecks(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	// Base commit
	mainFile := filepath.Join(dir, "app.go")
	_ = os.WriteFile(mainFile, []byte("package main\n\nfunc Run() {}\n"), 0o644)

	cmd := exec.Command("git", "add", "app.go")
	cmd.Dir = dir
	_ = cmd.Run()

	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	_ = cmd.Run()

	// Modify file with conflict marker, secret, and debug print
	modifiedSrc := `package main

import "fmt"

func Run() {
<<<<<<< HEAD
	apiKey := "AKIAIOSFODNN7EXAMPLE"
	fmt.Println("debugging run")
=======
	apiKey := "safe"
>>>>>>> branch
	_ = apiKey
}
`
	_ = os.WriteFile(mainFile, []byte(modifiedSrc), 0o644)

	env := &Env{Workspace: dir}
	report, err := ReviewDiff(context.Background(), env, DiffReviewOptions{
		StaticOnly: true,
	})
	if err != nil {
		t.Fatalf("ReviewDiff error: %v", err)
	}

	if len(report.FilesChanged) != 1 || report.FilesChanged[0] != "app.go" {
		t.Errorf("expected app.go in FilesChanged, got: %v", report.FilesChanged)
	}
	if report.Additions == 0 {
		t.Errorf("expected additions > 0, got: %d", report.Additions)
	}

	foundConflict := false
	foundSecret := false
	foundDebug := false

	for _, issue := range report.StaticIssues {
		switch issue.Kind {
		case DiffIssueConflict:
			foundConflict = true
		case DiffIssueSecret:
			foundSecret = true
		case DiffIssueDebug:
			foundDebug = true
		}
	}

	if !foundConflict {
		t.Errorf("expected merge conflict issue, got: %+v", report.StaticIssues)
	}
	if !foundSecret {
		t.Errorf("expected secret issue, got: %+v", report.StaticIssues)
	}
	if !foundDebug {
		t.Errorf("expected debug issue, got: %+v", report.StaticIssues)
	}

	txt := report.FormatText()
	if !strings.Contains(txt, "Static Inspections:") || !strings.Contains(txt, "[CRITICAL]") {
		t.Errorf("expected text format with CRITICAL static inspections, got:\n%s", txt)
	}

	jsonStr, err := report.FormatJSON()
	if err != nil {
		t.Fatalf("FormatJSON error: %v", err)
	}
	if !strings.Contains(jsonStr, `"kind": "conflict"`) {
		t.Errorf("expected json with conflict issue, got:\n%s", jsonStr)
	}
}

func TestReviewDiff_CleanDiff(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)

	cleanFile := filepath.Join(dir, "clean.go")
	_ = os.WriteFile(cleanFile, []byte("package main\n\nfunc Hello() string { return \"hi\" }\n"), 0o644)

	cmd := exec.Command("git", "add", "clean.go")
	cmd.Dir = dir
	_ = cmd.Run()

	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	_ = cmd.Run()

	// Clean modification
	_ = os.WriteFile(cleanFile, []byte("package main\n\nfunc Hello() string { return \"hello world\" }\n"), 0o644)

	env := &Env{Workspace: dir}
	report, err := ReviewDiff(context.Background(), env, DiffReviewOptions{
		StaticOnly: true,
	})
	if err != nil {
		t.Fatalf("ReviewDiff error: %v", err)
	}

	if len(report.StaticIssues) != 0 {
		t.Errorf("expected 0 static issues for clean diff, got: %d", len(report.StaticIssues))
	}

	txt := report.FormatText()
	if !strings.Contains(txt, "PASS (no conflict markers") {
		t.Errorf("expected PASS status, got:\n%s", txt)
	}
}
