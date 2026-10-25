package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

func TestDiagnoseGoFile(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Workspace: dir}

	goodPath := filepath.Join(dir, "good.go")
	if err := os.WriteFile(goodPath, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	badPath := filepath.Join(dir, "bad.go")
	if err := os.WriteFile(badPath, []byte("package main\n\nfunc broken( {\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if diag := DiagnoseFile(env, "good.go"); diag != "" {
		t.Fatalf("expected clean go file to produce no diagnostics, got: %s", diag)
	}

	badDiag := DiagnoseFile(env, "bad.go")
	if badDiag == "" {
		t.Fatalf("expected syntax error diagnostics for bad.go")
	}
	if !strings.Contains(badDiag, "Go syntax error") {
		t.Errorf("diagnostic should mention Go syntax error, got: %s", badDiag)
	}
}

func TestDiagnoseJSONFile(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Workspace: dir}

	goodPath := filepath.Join(dir, "good.json")
	if err := os.WriteFile(goodPath, []byte(`{"name":"termixgo","valid":true}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, []byte(`{"name":"termixgo",,}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if diag := DiagnoseFile(env, "good.json"); diag != "" {
		t.Fatalf("expected clean json file to produce no diagnostics, got: %s", diag)
	}

	badDiag := DiagnoseFile(env, "bad.json")
	if badDiag == "" || !strings.Contains(badDiag, "invalid JSON syntax") {
		t.Fatalf("expected invalid JSON syntax diagnostic, got: %s", badDiag)
	}
}

func TestDiagnoseYAMLFile(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Workspace: dir}

	goodPath := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(goodPath, []byte("key: value\nlist:\n  - a\n  - b\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	badPath := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(badPath, []byte("key: [unclosed\n  bad: map\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if diag := DiagnoseFile(env, "good.yaml"); diag != "" {
		t.Fatalf("expected clean yaml file to produce no diagnostics, got: %s", diag)
	}

	badDiag := DiagnoseFile(env, "bad.yaml")
	if badDiag == "" || !strings.Contains(badDiag, "invalid YAML syntax") {
		t.Fatalf("expected invalid YAML syntax diagnostic, got: %s", badDiag)
	}
}

func TestDiagnosePathsAggregation(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Workspace: dir}

	p1 := filepath.Join(dir, "clean.go")
	_ = os.WriteFile(p1, []byte("package p1\n"), 0o644)
	p2 := filepath.Join(dir, "broken.go")
	_ = os.WriteFile(p2, []byte("package p2\nfunc broken( {\n"), 0o644)

	res := DiagnosePaths(env, []string{"clean.go", "broken.go"})
	if !strings.Contains(res, "broken.go") {
		t.Errorf("aggregated diagnostics must mention broken.go, got: %s", res)
	}
	if strings.Contains(res, "clean.go") {
		t.Errorf("aggregated diagnostics should not mention clean.go, got: %s", res)
	}

	cleanRes := DiagnosePaths(env, []string{"clean.go"})
	if cleanRes != "" {
		t.Errorf("all clean files must produce empty result, got: %s", cleanRes)
	}
}

func TestCheckDiagnosticsTool(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Workspace: dir}

	_ = os.WriteFile(filepath.Join(dir, "clean.go"), []byte("package clean\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "syntax_err.go"), []byte("package foo\nfunc (\n"), 0o644)

	tool := &checkDiagnosticsTool{}

	// Test single clean file
	resClean, err := tool.Run(context.Background(), env, map[string]any{"path": "clean.go"})
	if err != nil || resClean.IsError {
		t.Fatalf("clean run unexpected error: %v, output: %s", err, resClean.Output)
	}
	if !strings.Contains(resClean.Output, "No syntax errors found") {
		t.Errorf("clean output expected no syntax errors found, got: %s", resClean.Output)
	}

	// Test single broken file
	resBroken, err := tool.Run(context.Background(), env, map[string]any{"path": "syntax_err.go"})
	if err != nil || !resBroken.IsError {
		t.Fatalf("broken file should have IsError true")
	}
	if !strings.Contains(resBroken.Output, "Go syntax error") {
		t.Errorf("expected Go syntax error, got: %s", resBroken.Output)
	}

	// Test directory walk
	resDir, err := tool.Run(context.Background(), env, map[string]any{"path": "."})
	if err != nil || !resDir.IsError {
		t.Fatalf("dir walk with broken file should have IsError true")
	}
	if !strings.Contains(resDir.Output, "syntax_err.go") {
		t.Errorf("dir output should report syntax_err.go, got: %s", resDir.Output)
	}
}

func TestPostEditDiagnosticsIntegratedInRunnerExecute(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "code.go")
	_ = os.WriteFile(filePath, []byte("package main\n\nfunc main() {}\n"), 0o644)

	env := &Env{
		Workspace: dir,
		Config:    config.Config{},
		Trusted:   true,
	}

	runner := &Runner{
		Env:   env,
		Tools: DefaultRegistry(),
	}

	// 1. Valid edit: change func main() to func hello()
	validCall := provider.ToolCall{
		Name:      "edit",
		Arguments: `{"path":"code.go","old_string":"func main() {}","new_string":"func hello() {}"}`,
	}
	validResult := runner.execute(context.Background(), validCall)
	if validResult.IsError {
		t.Fatalf("unexpected edit error: %s", validResult.Output)
	}
	if strings.Contains(validResult.Output, "[Diagnostics:") {
		t.Errorf("valid edit should not have diagnostics note, got: %s", validResult.Output)
	}

	// 2. Syntax-breaking edit: replace with broken syntax
	badCall := provider.ToolCall{
		Name:      "edit",
		Arguments: `{"path":"code.go","old_string":"func hello() {}","new_string":"func broken( {"}`,
	}
	badResult := runner.execute(context.Background(), badCall)
	if badResult.IsError {
		t.Fatalf("edit itself should succeed on disk: %s", badResult.Output)
	}
	if !strings.Contains(badResult.Output, "[Diagnostics: syntax error(s) detected after change:") {
		t.Fatalf("broken edit must append diagnostics notice, got: %s", badResult.Output)
	}
	if !strings.Contains(badResult.Output, "Go syntax error") {
		t.Errorf("should name Go syntax error, got: %s", badResult.Output)
	}

	// 3. Config with PostEditDiagnosticsDisabled: true suppresses diagnostics
	env.Config.PostEditDiagnosticsDisabled = true
	disabledResult := runner.execute(context.Background(), provider.ToolCall{
		Name:      "write_file",
		Arguments: `{"path":"broken2.go","content":"package broken\nfunc (\n"}`,
	})
	if strings.Contains(disabledResult.Output, "[Diagnostics:") {
		t.Errorf("diagnostics should be suppressed when PostEditDiagnosticsDisabled is true, got: %s", disabledResult.Output)
	}
}
