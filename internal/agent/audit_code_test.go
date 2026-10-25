package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditWorkspaceDetectsVulnerabilities(t *testing.T) {
	dir := t.TempDir()

	vulnFile := filepath.Join(dir, "server.go")
	vulnCode := `package main

import (
	"crypto/tls"
	"fmt"
	"os/exec"
)

func insecureConnect() {
	_ = &tls.Config{InsecureSkipVerify: true}
}

func runShell(cmd string) {
	_ = exec.Command("bash", "-c", fmt.Sprintf("echo %s", cmd))
}
`
	if err := os.WriteFile(vulnFile, []byte(vulnCode), 0o644); err != nil {
		t.Fatalf("write vulnFile: %v", err)
	}

	cleanFile := filepath.Join(dir, "clean.go")
	cleanCode := `package main

func add(a, b int) int {
	return a + b
}
`
	if err := os.WriteFile(cleanFile, []byte(cleanCode), 0o644); err != nil {
		t.Fatalf("write cleanFile: %v", err)
	}

	report, err := AuditWorkspace(dir, dir, AuditOptions{Category: "security"})
	if err != nil {
		t.Fatalf("AuditWorkspace: %v", err)
	}

	if report.FilesScanned != 2 {
		t.Errorf("expected 2 files scanned, got: %d", report.FilesScanned)
	}

	if len(report.Findings) < 2 {
		t.Fatalf("expected at least 2 findings, got %d", len(report.Findings))
	}

	foundTLS := false
	foundShell := false
	for _, f := range report.Findings {
		if f.RuleID == "VUL001" {
			foundTLS = true
			if f.Severity != SeverityHigh {
				t.Errorf("VUL001 should be HIGH severity, got: %s", f.Severity)
			}
		}
		if f.RuleID == "VUL002" {
			foundShell = true
			if f.Severity != SeverityCritical {
				t.Errorf("VUL002 should be CRITICAL severity, got: %s", f.Severity)
			}
		}
	}
	if !foundTLS {
		t.Errorf("expected to find VUL001 (InsecureSkipVerify)")
	}
	if !foundShell {
		t.Errorf("expected to find VUL002 (bash -c fmt.Sprintf)")
	}
}

func TestAuditWorkspaceDetectsSecrets(t *testing.T) {
	dir := t.TempDir()

	secretFile := filepath.Join(dir, "secrets.env")
	secretCode := `
AWS_KEY=AKIAIOSFODNN7EXAMPLE
`
	if err := os.WriteFile(secretFile, []byte(secretCode), 0o644); err != nil {
		t.Fatalf("write secretFile: %v", err)
	}

	report, err := AuditWorkspace(dir, dir, AuditOptions{Category: "secrets"})
	if err != nil {
		t.Fatalf("AuditWorkspace: %v", err)
	}

	if len(report.Findings) == 0 {
		t.Fatalf("expected to detect AWS Access Key secret")
	}

	foundAWS := false
	for _, f := range report.Findings {
		if f.RuleID == "SEC002" {
			foundAWS = true
		}
	}
	if !foundAWS {
		t.Errorf("expected SEC002 finding for AWS key")
	}
}

func TestAuditCodeTool(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Workspace: dir}

	_ = os.WriteFile(filepath.Join(dir, "app.go"), []byte("package main\nfunc ok() {}\n"), 0o644)

	tool := &auditCodeTool{}
	result, err := tool.Run(context.Background(), env, map[string]any{"category": "all"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected clean run, got error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "No security vulnerabilities") {
		t.Errorf("expected clean audit output, got: %s", result.Output)
	}
}

func TestFormatAuditJSON(t *testing.T) {
	report := &AuditReport{
		Workspace:    "c:/test",
		FilesScanned: 10,
		Findings: []AuditFinding{
			{
				Category:    CategorySecurity,
				Severity:    SeverityCritical,
				File:        "main.go",
				Line:        15,
				RuleID:      "VUL002",
				Description: "test desc",
			},
		},
	}

	jsonOut, err := FormatAuditJSON(report)
	if err != nil {
		t.Fatalf("FormatAuditJSON: %v", err)
	}
	if !strings.Contains(jsonOut, `"rule_id": "VUL002"`) {
		t.Errorf("json output missing rule_id: %s", jsonOut)
	}
}
