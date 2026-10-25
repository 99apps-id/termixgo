package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAuditCodeCommand(t *testing.T) {
	dir := t.TempDir()
	originalWd, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(originalWd) }()

	goodFile := filepath.Join(dir, "math.go")
	_ = os.WriteFile(goodFile, []byte("package main\nfunc Add(a, b int) int { return a + b }\n"), 0o644)

	var stdout bytes.Buffer
	err := runAuditCode([]string{"--path", dir}, &stdout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Code & Security Audit Report") {
		t.Errorf("expected audit header in output, got: %s", out)
	}
	if !strings.Contains(out, "No security vulnerabilities or code smells detected") {
		t.Errorf("expected clean audit message, got: %s", out)
	}

	// Test JSON output
	var jsonStdout bytes.Buffer
	err = runAuditCode([]string{"--path", dir, "--json"}, &jsonStdout)
	if err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	if !strings.Contains(jsonStdout.String(), `"files_scanned": 1`) {
		t.Errorf("json output missing files_scanned: %s", jsonStdout.String())
	}

	// Test delegation from runAudit
	var delegStdout bytes.Buffer
	err = runAudit([]string{"code", "--path", dir}, &delegStdout)
	if err != nil {
		t.Fatalf("unexpected delegation error: %v", err)
	}
	if !strings.Contains(delegStdout.String(), "Code & Security Audit Report") {
		t.Errorf("expected delegated audit report, got: %s", delegStdout.String())
	}
}
