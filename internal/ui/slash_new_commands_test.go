package ui

import (
	"os"
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
