package ui

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestOSC52EncodesThePayload(t *testing.T) {
	got := osc52("hello")
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("hello")) + "\x07"
	if got != want {
		t.Errorf("osc52 = %q, want %q", got, want)
	}
}

func TestClampClipboardKeepsRunesWhole(t *testing.T) {
	text := strings.Repeat("é", osc52MaxBytes)
	clamped := clampClipboard(text)
	if len(clamped) > osc52MaxBytes {
		t.Errorf("clamped length = %d, want at most %d", len(clamped), osc52MaxBytes)
	}
	if !strings.HasPrefix(text, clamped) {
		t.Errorf("clamping must keep a prefix")
	}
	// The prefix has to stay valid UTF-8, so it does not teach the terminal bad bytes.
	if strings.ContainsRune(clamped, '\uFFFD') {
		t.Errorf("clamped text contains a replacement rune")
	}
}

func TestSlashCopyCopiesTheLastAnswer(t *testing.T) {
	model := chatModel(t)
	model.blocks = []block{
		{kind: blockUser, text: "hi"},
		{kind: blockAssistant, text: "the answer"},
	}

	updated, cmd := model.slashCopy("")
	if cmd == nil {
		t.Fatalf("copying an answer must return a command")
	}
	after := updated.(*Model)
	if after.lastAssistantText() != "the answer" {
		t.Errorf("lastAssistantText = %q", after.lastAssistantText())
	}
	found := false
	for _, entry := range after.blocks {
		if entry.kind == blockNotice && strings.Contains(entry.text, "Copied") {
			found = true
		}
	}
	if !found {
		t.Errorf("the copy should be acknowledged")
	}
}

func TestSlashCopyWithNothingToCopy(t *testing.T) {
	model := chatModel(t)
	model.blocks = nil

	updated, cmd := model.slashCopy("")
	if cmd != nil {
		t.Errorf("nothing to copy must not write to the clipboard")
	}
	after := updated.(*Model)
	found := false
	for _, entry := range after.blocks {
		if entry.kind == blockError {
			found = true
		}
	}
	if !found {
		t.Errorf("an empty transcript should explain itself")
	}
}

func TestTranscriptTextIncludesTurnsAndTools(t *testing.T) {
	model := chatModel(t)
	model.blocks = []block{
		{kind: blockUser, text: "do it"},
		{kind: blockAssistant, text: "done"},
		{kind: blockTool, toolName: "read_file", toolResult: "contents"},
	}
	text := model.transcriptText()
	for _, want := range []string{"You:\ndo it", "Assistant:\ndone", "Tool read_file:\ncontents"} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript missing %q:\n%s", want, text)
		}
	}
}
