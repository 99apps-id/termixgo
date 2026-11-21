package agent

import (
	"strings"
	"testing"
)

func TestToolOutputPreviewIsForCommandsAndFailures(t *testing.T) {
	long := strings.Repeat("line\n", 100)
	if head, total := toolOutputPreview(&runCommandTool{}, Result{Output: long}); total != 100 || strings.Count(head, "\n") != toolPreviewLines-1 {
		t.Fatalf("command preview: %d lines total, head has %d", total, strings.Count(head, "\n")+1)
	}
	if head, _ := toolOutputPreview(&readFileTool{}, Result{Output: long}); head != "" {
		t.Fatalf("a file read must not be previewed")
	}
	if head, _ := toolOutputPreview(&readFileTool{}, Result{Output: "no such file", IsError: true}); head != "no such file" {
		t.Fatalf("a failure is always previewed, got %q", head)
	}
	if head, _ := toolOutputPreview(&runCommandTool{}, Result{Output: "  \n"}); head != "" {
		t.Fatalf("empty output has no preview")
	}
}
