package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlashAuditCode(t *testing.T) {
	model := chatModel(t)
	_ = os.WriteFile(filepath.Join(model.app.Workspace(), "main.go"), []byte("package main\nfunc main() {}\n"), 0o644)

	_, output := runSlash(t, model, "/audit code")
	if !strings.Contains(output, "Code & Security Audit Report") {
		t.Errorf("expected audit report, got: %s", output)
	}
}

func TestSlashDiagnostics(t *testing.T) {
	model := chatModel(t)
	_ = os.WriteFile(filepath.Join(model.app.Workspace(), "clean.go"), []byte("package clean\n"), 0o644)

	_, output := runSlash(t, model, "/diagnostics clean.go")
	if !strings.Contains(output, "No syntax errors found") {
		t.Errorf("expected clean diagnostics, got: %s", output)
	}
}

func TestSlashEval(t *testing.T) {
	model := chatModel(t)

	_, output := runSlash(t, model, "/eval list")
	if !strings.Contains(output, "Evaluation Benchmark Suite") {
		t.Errorf("expected eval suite listing, got: %s", output)
	}
}

func TestSlashRepoMap(t *testing.T) {
	model := chatModel(t)
	_ = os.WriteFile(filepath.Join(model.app.Workspace(), "api.go"), []byte("package api\n\ntype Client struct{}\nfunc NewClient() *Client { return nil }\n"), 0o644)

	_, output := runSlash(t, model, "/map")
	if !strings.Contains(output, "api.go:") || !strings.Contains(output, "type Client struct") {
		t.Errorf("expected repo map with api.go, got: %s", output)
	}
}

func TestSlashTestGen(t *testing.T) {
	model := chatModel(t)
	_ = os.WriteFile(filepath.Join(model.app.Workspace(), "math.go"), []byte("package math\n\nfunc Square(n int) int { return n * n }\n"), 0o644)

	_, output := runSlash(t, model, "/testgen math.go")
	if !strings.Contains(output, "Synthesized unit tests") || !strings.Contains(output, "TestSquare") {
		t.Errorf("expected testgen output with TestSquare, got: %s", output)
	}
}

func TestSlashReview(t *testing.T) {
	model := chatModel(t)
	workspace := model.app.Workspace()

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		_ = cmd.Run()
	}
	runGit("init", "-q")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "test")
	runGit("commit", "--allow-empty", "-qm", "init")

	_, output := runSlash(t, model, "/diff-review static")
	if !strings.Contains(output, "No changes detected") && !strings.Contains(output, "Git Diff Review Report") {
		t.Errorf("expected review report output, got: %s", output)
	}
}
