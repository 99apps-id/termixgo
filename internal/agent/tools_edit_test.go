package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEditFuzzyMatch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "main.go")
	_ = os.WriteFile(target, []byte("package main\nfunc main() {}\n"), 0o644)

	env := &Env{Workspace: dir}
	args := map[string]any{
		"path":       "main.go",
		"old_string": "FUNC MAIN()",
		"new_string": "func Foo()",
		"fuzzy":      true,
	}

	tool := editTool{}
	result, err := tool.Run(context.Background(), env, args)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "package main\nfunc Foo() {}\n" {
		t.Fatalf("got %q", string(data))
	}
	if result.Output != "Applied 1 replacement(s) in main.go" {
		t.Fatalf("unexpected output: %s", result.Output)
	}
}
