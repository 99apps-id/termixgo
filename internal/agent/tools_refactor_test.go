package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefactorRenamePreviewAndExecute(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Workspace: dir}

	file1 := filepath.Join(dir, "calc.go")
	_ = os.WriteFile(file1, []byte("package main\n\nfunc OldCompute(x int) int {\n\treturn x * 2\n}\n"), 0o644)

	file2 := filepath.Join(dir, "main.go")
	_ = os.WriteFile(file2, []byte("package main\n\nfunc main() {\n\tres := OldCompute(10)\n\tprintln(res)\n}\n"), 0o644)

	tool := &refactorRenameTool{}

	// 1. Preview mode
	prevRes, err := tool.Run(context.Background(), env, map[string]any{
		"symbol":   "OldCompute",
		"new_name": "NewCompute",
		"preview":  true,
	})
	if err != nil || prevRes.IsError {
		t.Fatalf("preview unexpected failure: %v, %s", err, prevRes.Output)
	}
	if !strings.Contains(prevRes.Output, "Preview: renaming \"OldCompute\" to \"NewCompute\"") {
		t.Errorf("preview output mismatch: %s", prevRes.Output)
	}

	// Ensure files were NOT modified in preview
	content1, _ := os.ReadFile(file1)
	if !strings.Contains(string(content1), "OldCompute") {
		t.Errorf("preview mode must not modify file1")
	}

	// 2. Real execute
	execRes, err := tool.Run(context.Background(), env, map[string]any{
		"symbol":   "OldCompute",
		"new_name": "NewCompute",
	})
	if err != nil || execRes.IsError {
		t.Fatalf("execute unexpected failure: %v, %s", err, execRes.Output)
	}
	if !strings.Contains(execRes.Output, "Renamed \"OldCompute\" to \"NewCompute\" across 2 occurrence(s) in 2 file(s)") {
		t.Errorf("execute output mismatch: %s", execRes.Output)
	}

	// Verify both files updated
	c1, _ := os.ReadFile(file1)
	if !strings.Contains(string(c1), "func NewCompute(x int)") || strings.Contains(string(c1), "OldCompute") {
		t.Errorf("file1 not properly updated: %s", string(c1))
	}
	c2, _ := os.ReadFile(file2)
	if !strings.Contains(string(c2), "res := NewCompute(10)") || strings.Contains(string(c2), "OldCompute") {
		t.Errorf("file2 not properly updated: %s", string(c2))
	}
}

func TestRefactorRenameValidation(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Workspace: dir}
	tool := &refactorRenameTool{}

	// Identical names
	res, _ := tool.Run(context.Background(), env, map[string]any{"symbol": "Foo", "new_name": "Foo"})
	if !res.IsError || !strings.Contains(res.Output, "identical") {
		t.Errorf("expected identical error, got: %v", res)
	}

	// Invalid identifier syntax
	res, _ = tool.Run(context.Background(), env, map[string]any{"symbol": "Foo-Bar", "new_name": "Baz"})
	if !res.IsError || !strings.Contains(res.Output, "valid identifier") {
		t.Errorf("expected invalid identifier error, got: %v", res)
	}
}
