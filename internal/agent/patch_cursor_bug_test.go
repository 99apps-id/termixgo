package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchMultiHunkAdvancesCursor(t *testing.T) {
	env, workspace := patchEnv(t)
	path := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3\nline4\nline5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	document := "*** Begin Patch\n" +
		"*** Update File: sample.txt\n@@\n line2\n-line3\n+line3 changed\n@@\n-line4\n+line4 changed\n line5\n" +
		"*** End Patch\n"
	tool := &applyPatchTool{}
	result, err := tool.Run(context.Background(), env, map[string]any{"patch": document})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("patch failed: %q", result.Output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	expected := "line1\nline2\nline3 changed\nline4 changed\nline5\n"
	if string(data) != expected {
		t.Errorf("patch did not advance cursor correctly:\ngot %q\nwant %q", string(data), expected)
	}
}
