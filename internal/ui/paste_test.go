package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func pasteKey(text string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text), Paste: true}
}

func TestLongPasteCollapsesToAPlaceholder(t *testing.T) {
	model := chatModel(t)
	long := strings.Repeat("log line\n", 41) + "end"
	next, _ := model.Update(pasteKey(long))
	updated := next.(*Model)
	if got := updated.composer.Value(); got != "[Pasted text #1 +42 lines]" {
		t.Fatalf("composer = %q", got)
	}
	if updated.expandPastes(updated.composer.Value()) != long {
		t.Fatalf("the placeholder should expand to the whole paste")
	}
}

func TestShortPasteStaysInline(t *testing.T) {
	model := chatModel(t)
	next, _ := model.Update(pasteKey("two\nlines"))
	if got := next.(*Model).composer.Value(); got != "two\nlines" {
		t.Fatalf("composer = %q", got)
	}
}

// The transcript shows the placeholder, and a queued input keeps its paste
// until it is sent.
func TestPastePlaceholderAcrossQueueAndSend(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.collapsePaste(strings.Repeat("x\n", 10))
	value := "explain " + model.composer.Value()
	model.queue = append(model.queue, value)
	model.forgetPastes("unrelated")
	if model.expandPastes(value) == value {
		t.Fatalf("a queued input must keep its paste")
	}
	model.queue = nil
	model.forgetPastes(value)
	if len(model.pastes) != 0 {
		t.Fatalf("a sent paste should be forgotten, %d left", len(model.pastes))
	}
	if model.expandPastes("[Pasted text #9 +3 lines]") != "[Pasted text #9 +3 lines]" {
		t.Fatalf("an unknown placeholder is left as typed")
	}
}
