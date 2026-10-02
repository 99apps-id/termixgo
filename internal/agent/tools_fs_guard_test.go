package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestListDirectoryDefaultsToTheWorkspace covers the documented fallback. The
// workspace check rejects an empty path because it cannot be made relative to
// anything, so asking for the path before substituting the workspace root made
// the fallback dead and the call fail with a raw Go error.
func TestListDirectoryDefaultsToTheWorkspace(t *testing.T) {
	env := testEnv(t)
	if err := os.WriteFile(filepath.Join(env.Workspace, "visible.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := &listDirectoryTool{}
	result, err := tool.Run(context.Background(), env, map[string]any{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("an omitted path should list the workspace, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "visible.go") {
		t.Errorf("the listing should hold the workspace entries:\n%s", result.Output)
	}
}

// TestDeleteFileRefusesTheWorkspaceRoot is the guard for an unrecoverable
// mistake: `delete_file` with "." resolved to the workspace root and removed
// every file in it, then reported success.
func TestDeleteFileRefusesTheWorkspaceRoot(t *testing.T) {
	env := testEnv(t)
	keep := filepath.Join(env.Workspace, "keep.txt")
	if err := os.WriteFile(keep, []byte("precious\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := &deleteFileTool{}
	for _, raw := range []string{".", "./", env.Workspace} {
		result, err := tool.Run(context.Background(), env, map[string]any{"path": raw})
		if err != nil {
			t.Fatalf("Run(%q): %v", raw, err)
		}
		if !result.IsError || !strings.Contains(result.Output, "workspace root") {
			t.Errorf("Run(%q) should refuse, got isError=%v %q", raw, result.IsError, result.Output)
		}
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("Run(%q) removed the workspace contents: %v", raw, err)
		}
	}

	// A path inside the workspace still works, so the guard is not a blanket.
	if err := os.MkdirAll(filepath.Join(env.Workspace, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	result, err := tool.Run(context.Background(), env, map[string]any{"path": "sub"})
	if err != nil {
		t.Fatalf("Run(sub): %v", err)
	}
	if result.IsError {
		t.Errorf("deleting a subdirectory should still work, got %q", result.Output)
	}
}

// TestMoveFileRefusesTheWorkspaceRoot keeps the same mistake from breaking the
// session: moving the root takes the working directory with it, so every later
// tool call fails for a reason the model cannot see.
func TestMoveFileRefusesTheWorkspaceRoot(t *testing.T) {
	env := testEnv(t)
	tool := &moveFileTool{}
	result, err := tool.Run(context.Background(), env, map[string]any{"from": ".", "to": "moved"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Output, "workspace root") {
		t.Errorf("moving the root should be refused, got isError=%v %q", result.IsError, result.Output)
	}
	if _, err := os.Stat(env.Workspace); err != nil {
		t.Errorf("the workspace should still exist: %v", err)
	}
}

// TestWriteFileRefusesABrokenSymlinkEscape closes the write path that let a
// symlink whose target did not exist yet pass the workspace check: the check
// fell back to resolving only the parent directory, so the write then followed
// the symlink and created a file outside the workspace.
func TestWriteFileRefusesABrokenSymlinkEscape(t *testing.T) {
	env := testEnv(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "newfile")
	link := filepath.Join(env.Workspace, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	result, err := (&writeFileTool{}).Run(context.Background(), env, map[string]any{"path": "escape", "content": "pwned"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsError {
		t.Fatalf("writing through a broken symlink that escapes the workspace should be refused, got %q", result.Output)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatalf("the write escaped the workspace and created %s", target)
	}
}

// TestMemoryCapKeepsValidUTF8 covers the trim branch of the learned-memory file.
// The cap was applied with a byte slice, so a multi-byte fact could be cut in
// half and the file written with invalid UTF-8 that the prompt then carries.
func TestMemoryCapKeepsValidUTF8(t *testing.T) {
	mem := NewMemory(t.TempDir())
	for index := 0; index < maxMemoryFacts; index++ {
		fact := fmt.Sprintf("fact %03d ", index) + strings.Repeat("\u00e9", 200)
		if err := mem.Remember(fact, "project"); err != nil {
			t.Fatalf("Remember %d: %v", index, err)
		}
	}

	data, err := os.ReadFile(mem.projectPath())
	if err != nil {
		t.Fatalf("read memory: %v", err)
	}
	if !utf8.Valid(data) {
		t.Errorf("the memory file holds invalid UTF-8")
	}
	if !strings.HasPrefix(string(data), "# Termixgo memory\n\n- ") {
		t.Errorf("the file should keep its header and the first fact:\n%q", firstLine(string(data)))
	}
	if len(data) > maxMemoryBytes {
		t.Errorf("the file is %d bytes, over the %d cap", len(data), maxMemoryBytes)
	}
	// Every stored fact must still be readable, which is what the prompt uses.
	project, _ := mem.Read()
	if len(project) == 0 {
		t.Errorf("the facts should survive the trim")
	}
	for _, fact := range project {
		if !utf8.ValidString(fact) {
			t.Errorf("a stored fact is invalid UTF-8: %q", fact)
		}
	}
}
