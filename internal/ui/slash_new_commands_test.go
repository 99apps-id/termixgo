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

func TestSlashWhatsApp(t *testing.T) {
	model := chatModel(t)
	_, output := runSlash(t, model, "/whatsapp status")
	if !strings.Contains(output, "WhatsApp") {
		t.Errorf("expected WhatsApp status, got: %s", output)
	}
}

func TestSlashChannels(t *testing.T) {
	model := chatModel(t)
	_, output := runSlash(t, model, "/channels status")
	if !strings.Contains(output, "channels") && !strings.Contains(output, "Channels") {
		t.Errorf("expected channels status, got: %s", output)
	}
}

func TestSlashTheme(t *testing.T) {
	model := chatModel(t)
	_, output := runSlash(t, model, "/theme")
	if !strings.Contains(output, "Current theme:") {
		t.Errorf("expected theme output, got: %s", output)
	}
	_, switched := runSlash(t, model, "/theme light")
	if !strings.Contains(switched, "Theme switched to light") {
		t.Errorf("expected theme switch to light, got: %s", switched)
	}
	if model.app.Config().Theme != "light" {
		t.Errorf("config Theme = %q, want light", model.app.Config().Theme)
	}
}

func TestLocalShellExecution(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("!echo hello-world")
	res := press(t, model, "enter")
	text := allText(res)
	if !strings.Contains(text, "hello-world") {
		t.Errorf("expected local shell command execution to print hello-world, got:\n%s", text)
	}
}

func TestTUIShortcuts(t *testing.T) {
	model := chatModel(t)
	// Ctrl+T toggles thinking
	initThinking := model.app.Config().ShowReasoning
	res := press(t, model, "ctrl+t")
	if res.app.Config().ShowReasoning == initThinking {
		t.Errorf("ctrl+t should toggle thinking")
	}

	// Ctrl+L opens model picker
	res = press(t, model, "ctrl+l")
	if res.current != modePicker {
		t.Errorf("ctrl+l should open picker, got mode %v", res.current)
	}

	// Ctrl+P opens session picker
	model2 := chatModel(t)
	res = press(t, model2, "ctrl+p")
	if res.current != modePicker && !strings.Contains(allText(res), "No saved sessions") {
		t.Errorf("ctrl+p should open session picker or notice")
	}

	// Ctrl+G opens agent picker
	model3 := chatModel(t)
	res = press(t, model3, "ctrl+g")
	if res.current != modePicker {
		t.Errorf("ctrl+g should open agent picker, got mode %v", res.current)
	}
}
