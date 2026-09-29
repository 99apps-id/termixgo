package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFrameLogRecordsPaints proves the diagnostic: with TERMIXGO_FRAMELOG
// set, a refresh appends the transcript text, capped and plain.
func TestFrameLogRecordsPaints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frames.log")
	t.Setenv("TERMIXGO_FRAMELOG", path)

	model := chatModel(t)
	model.blocks = append(model.blocks, block{kind: blockAssistant, text: "hello world"})
	model.refresh()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "hello world") {
		t.Errorf("the log should hold the painted text:\n%s", data)
	}
}

// TestFrameLogStaysOffByDefault keeps the hot path clean: without the env
// var no file appears.
func TestFrameLogStaysOffByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frames.log")
	t.Setenv("TERMIXGO_FRAMELOG", "")
	logFrame("anything")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("no log file should exist")
	}
}
