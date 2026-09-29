package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func patchEnv(t *testing.T) (*Env, string) {
	t.Helper()
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true}
	return env, workspace
}

func TestApplyPatchUpdatesTwoFilesAtOnce(t *testing.T) {
	env, workspace := patchEnv(t)
	if err := os.WriteFile(filepath.Join(workspace, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	document := "*** Begin Patch\n" +
		"*** Update File: a.go\n@@\n package a\n \n-func A() {}\n+func A() int {\n+return 1\n+}\n" +
		"*** Update File: b.go\n@@\n package b\n+// added\n" +
		"*** End Patch\n"
	tool := &applyPatchTool{}
	result, err := tool.Run(context.Background(), env, map[string]any{"patch": document})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("result is an error: %q", result.Output)
	}
	data, _ := os.ReadFile(filepath.Join(workspace, "a.go"))
	if !strings.Contains(string(data), "func A() int") {
		t.Errorf("a.go was not patched:\n%s", data)
	}
	data, _ = os.ReadFile(filepath.Join(workspace, "b.go"))
	if !strings.Contains(string(data), "// added") {
		t.Errorf("b.go was not patched:\n%s", data)
	}
}

func TestApplyPatchAddsAndDeletes(t *testing.T) {
	env, workspace := patchEnv(t)
	if err := os.WriteFile(filepath.Join(workspace, "old.txt"), []byte("gone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	document := "*** Begin Patch\n*** Add File: new/dir.txt\n@@\n+hello\n*** Delete File: old.txt\n*** End Patch\n"
	tool := &applyPatchTool{}
	if result, _ := tool.Run(context.Background(), env, map[string]any{"patch": document}); result.IsError {
		t.Fatalf("result is an error: %q", result.Output)
	}
	if _, err := os.Stat(filepath.Join(workspace, "new", "dir.txt")); err != nil {
		t.Errorf("the added file is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "old.txt")); err == nil {
		t.Errorf("the deleted file is still there")
	}
}

func TestApplyPatchRefusesBadContext(t *testing.T) {
	env, workspace := patchEnv(t)
	if err := os.WriteFile(filepath.Join(workspace, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	document := "*** Begin Patch\n*** Update File: a.go\n@@\n-wrong context\n+new\n*** End Patch\n"
	tool := &applyPatchTool{}
	result, _ := tool.Run(context.Background(), env, map[string]any{"patch": document})
	if !result.IsError {
		t.Errorf("a hunk that matches nowhere must fail")
	}
}

func TestApplyPatchRefusesOutsideAndEmpty(t *testing.T) {
	env, _ := patchEnv(t)
	tool := &applyPatchTool{}
	for _, document := range []string{
		"no markers here",
		"*** Begin Patch\n*** End Patch\n",
		"*** Begin Patch\n*** Update File: ../escape.txt\n@@\n+x\n*** End Patch\n",
	} {
		result, _ := tool.Run(context.Background(), env, map[string]any{"patch": document})
		if !result.IsError {
			t.Errorf("patch should fail:\n%s", document)
		}
	}
}

func TestApplyPatchRefusesAddOverExisting(t *testing.T) {
	env, workspace := patchEnv(t)
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	document := "*** Begin Patch\n*** Add File: a.txt\n@@\n+other\n*** End Patch\n"
	tool := &applyPatchTool{}
	result, _ := tool.Run(context.Background(), env, map[string]any{"patch": document})
	if !result.IsError {
		t.Errorf("adding over a file must fail")
	}
}
