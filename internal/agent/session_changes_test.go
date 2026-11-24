package agent

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

func toolCall(id, name, args string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: id, Name: name, Arguments: args}}}
}

func toolResult(id, name, output string) provider.Message {
	return provider.Message{Role: provider.RoleTool, ToolID: id, Name: name, Content: output}
}

func TestSummarizeChangesCountsFilesAndLines(t *testing.T) {
	messages := []provider.Message{
		toolCall("1", "edit", `{"path":"a.go","old_string":"x := 1\ny := 2\n","new_string":"x := 1\ny := 3\nz := 4\n"}`),
		toolResult("1", "edit", "Edited a.go"),
		toolCall("2", "write_file", `{"path":"b.txt","content":"one\ntwo\nthree\n"}`),
		toolResult("2", "write_file", "Wrote b.txt"),
		toolCall("3", "edit", `{"path":"a.go","old_string":"a","new_string":"b"}`),
		toolResult("3", "edit", "Edited a.go"),
		toolCall("4", "apply_patch", "{\"patch\":\"*** Begin Patch\\n*** Update File: c.go\\n@@\\n keep\\n-old\\n+new\\n+more\\n*** Add File: d.go\\n+fresh\\n*** End Patch\"}"),
		toolResult("4", "apply_patch", "Applied:\n  update c.go"),
		toolCall("5", "delete_file", `{"path":"e.go"}`),
		toolResult("5", "delete_file", "Deleted e.go"),
	}
	got := SummarizeChanges(messages)
	// Files: a.go b.txt c.go d.go e.go. a.go: edit 1 adds 2 removes 1, edit 3 adds 1 removes 1.
	// b.txt adds 3. patch: c.go removes 1 adds 2, d.go adds 1.
	want := SessionChanges{Files: 5, Added: 2 + 1 + 3 + 2 + 1, Removed: 1 + 1 + 1}
	if got != want {
		t.Fatalf("changes = %+v, want %+v", got, want)
	}
}

// A refused edit changed nothing and must not be counted, and a read is not a
// change at all.
func TestSummarizeChangesIgnoresFailuresAndReads(t *testing.T) {
	messages := []provider.Message{
		toolCall("1", "edit", `{"path":"a.go","old_string":"x","new_string":"y"}`),
		toolResult("1", "edit", "error: old_string not found"),
		toolCall("2", "read_file", `{"path":"a.go"}`),
		toolResult("2", "read_file", "package a"),
		toolCall("3", "edit", `not json`),
	}
	if got := SummarizeChanges(messages); got != (SessionChanges{}) {
		t.Fatalf("changes = %+v, want none", got)
	}
}

func TestLineDeltaSetsAsideUnchangedEnds(t *testing.T) {
	added, removed := lineDelta("a\nb\nc\n", "a\nB\nc\n")
	if added != 1 || removed != 1 {
		t.Fatalf("a one line change read as +%d -%d", added, removed)
	}
	if added, removed := lineDelta("", "x\ny\n"); added != 2 || removed != 0 {
		t.Fatalf("an insertion read as +%d -%d", added, removed)
	}
}

// The runner appends an <env> block to every user turn; a title built from the
// turn must not carry it, and an old saved title is cleaned when read.
func TestSessionTitleLeavesOutTheEnvBlock(t *testing.T) {
	session := NewSession("w", "m")
	session.AddUser("selamat pagi\n\n<env>\nworkspace_root: C:/Project\nfolder_trust: trusted\n</env>")
	if got := session.Title(); got != "selamat pagi" {
		t.Fatalf("title = %q", got)
	}
	old := NewSession("w", "m")
	old.title = "selamat sore <env> workspace_root: C:/mywork/rascotec fol..."
	if got := old.Title(); got != "selamat sore" {
		t.Fatalf("an old title = %q", got)
	}
	bare := NewSession("w", "m")
	bare.AddUser("<env>\nworkspace_root: x\n</env>")
	if got := bare.Title(); got != "" {
		t.Fatalf("a turn with no words must stay untitled, got %q", got)
	}
}
