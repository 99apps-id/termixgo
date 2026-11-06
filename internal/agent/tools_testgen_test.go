package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateTestsTool_GoDryRun(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Workspace: dir}

	srcFile := filepath.Join(dir, "calc.go")
	src := `package calc

func Add(a, b int) int {
	return a + b
}
`
	if err := os.WriteFile(srcFile, []byte(src), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := &generateTestsTool{}
	res, err := tool.Run(context.Background(), env, map[string]any{
		"path":    "calc.go",
		"dry_run": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %s", res.Output)
	}

	if !strings.Contains(res.Output, "TestAdd(t *testing.T)") {
		t.Errorf("expected TestAdd in preview output, got:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "tests := []struct") {
		t.Errorf("expected table-driven test structure, got:\n%s", res.Output)
	}

	// In dry run, no test file should be written
	testFile := filepath.Join(dir, "calc_test.go")
	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Errorf("calc_test.go should not exist after dry run")
	}
}

func TestGenerateTestsTool_GoWriteFileAndDeduplicate(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Workspace: dir}

	srcFile := filepath.Join(dir, "math.go")
	src := `package math

type Multiplier struct {
	Factor int
}

func (m *Multiplier) Multiply(val int) int {
	return m.Factor * val
}

func Divide(a, b int) (int, error) {
	return a / b, nil
}
`
	if err := os.WriteFile(srcFile, []byte(src), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := &generateTestsTool{}
	res, err := tool.Run(context.Background(), env, map[string]any{
		"path": "math.go",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %s", res.Output)
	}

	testFile := filepath.Join(dir, "math_test.go")
	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("test file was not created: %v", err)
	}

	sContent := string(content)
	if !strings.Contains(sContent, "TestMultiplier_Multiply") {
		t.Errorf("expected TestMultiplier_Multiply, got:\n%s", sContent)
	}
	if !strings.Contains(sContent, "TestDivide") {
		t.Errorf("expected TestDivide, got:\n%s", sContent)
	}
	if !strings.Contains(sContent, "wantErr bool") {
		t.Errorf("expected wantErr in error-returning function test, got:\n%s", sContent)
	}

	// Second run: tests already exist, should report all functions already covered
	res2, err := tool.Run(context.Background(), env, map[string]any{
		"path": "math.go",
	})
	if err != nil {
		t.Fatalf("unexpected error on second run: %v", err)
	}
	if !strings.Contains(res2.Output, "already have corresponding tests") {
		t.Errorf("expected already have tests notice, got:\n%s", res2.Output)
	}
}

func TestGenerateTestsTool_Python(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Workspace: dir}

	srcFile := filepath.Join(dir, "processor.py")
	src := `def transform_text(text):
    return text.strip().lower()

def calculate_checksum(data):
    return len(data)
`
	if err := os.WriteFile(srcFile, []byte(src), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := &generateTestsTool{}
	res, err := tool.Run(context.Background(), env, map[string]any{
		"path": "processor.py",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %s", res.Output)
	}

	testFile := filepath.Join(dir, "test_processor.py")
	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("test file was not created: %v", err)
	}

	sContent := string(content)
	if !strings.Contains(sContent, "class TestProcessor(unittest.TestCase):") {
		t.Errorf("expected unittest TestCase class, got:\n%s", sContent)
	}
	if !strings.Contains(sContent, "def test_transform_text(self):") {
		t.Errorf("expected test_transform_text, got:\n%s", sContent)
	}
}

func TestGenerateTestsTool_Validation(t *testing.T) {
	dir := t.TempDir()
	env := &Env{Workspace: dir}
	tool := &generateTestsTool{}

	// Missing path
	res, err := tool.Run(context.Background(), env, map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Output, "path is required") {
		t.Errorf("expected path is required error, got: %v", res)
	}

	// Nonexistent file
	res, err = tool.Run(context.Background(), env, map[string]any{"path": "nonexistent.go"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Output, "file not found") {
		t.Errorf("expected file not found error, got: %v", res)
	}

	// Unsupported extension
	dummy := filepath.Join(dir, "readme.md")
	_ = os.WriteFile(dummy, []byte("# hello"), 0o644)
	res, err = tool.Run(context.Background(), env, map[string]any{"path": "readme.md"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Output, "not supported") {
		t.Errorf("expected not supported error, got: %v", res)
	}
}
