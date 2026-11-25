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
	if got := updated.composer.Value(); got != "[Pasted text #1 ~42 lines]" {
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

func TestShiftEnterInsertsNewline(t *testing.T) {
	// Bubble Tea v1.3.10 has no distinct Shift+Enter key (terminals
	// that send CSI u present it as a plain Enter), so in this terminal
	// the newline key is Ctrl+J. The composer must insert a newline on
	// it and keep the draft, which Enter (the default) does not.
	model := chatModel(t)
	model.composer.SetValue("hello")
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if got := next.(*Model).composer.Value(); got != "hello\n" {
		t.Fatalf("composer after Ctrl+Enter/Ctrl+J = %q, want hello plus a newline without a submit", got)
	}
}

func TestLargePasteSurvivesRoundTrip(t *testing.T) {
	model := chatModel(t)
	large := strings.Repeat("log line with data 123;456\n", 2000)
	next, _ := model.Update(pasteKey(large))
	updated := next.(*Model)
	if got := updated.expandPastes(updated.composer.Value()); got != large {
		t.Fatalf("large paste lost content: %d bytes in, %d out", len(large), len(got))
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
