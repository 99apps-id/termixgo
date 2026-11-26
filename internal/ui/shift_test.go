package ui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type fakeUnknownCSI []byte

func (u fakeUnknownCSI) String() string {
	return fmt.Sprintf("?CSI%+v?", []byte(u)[2:])
}

func TestShiftEnterViaModifierStateOnRealTerminal(t *testing.T) {
	orig := isShiftPressed
	t.Cleanup(func() { isShiftPressed = orig })

	// Shift held + Enter on a real terminal inserts a newline instead of submitting.
	isShiftPressed = func() bool { return true }
	model := chatModel(t)
	model.realTerminal = true
	model.composer.SetValue("baris 1")
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*Model)
	if got := updated.composer.Value(); got != "baris 1\n" {
		t.Fatalf("composer after Shift+Enter = %q, want %q", got, "baris 1\n")
	}
	if updated.running {
		t.Fatal("Shift+Enter must not submit the draft")
	}

	// Shift NOT held + Enter still submits.
	isShiftPressed = func() bool { return false }
	next, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	submitted := next.(*Model)
	if got := submitted.composer.Value(); got != "" {
		t.Fatalf("composer after bare Enter = %q, want empty (submitted)", got)
	}
}

func TestShiftEnterIgnoredWhenNotRealTerminal(t *testing.T) {
	orig := isShiftPressed
	t.Cleanup(func() { isShiftPressed = orig })
	isShiftPressed = func() bool { return true }

	model := chatModel(t)
	model.realTerminal = false
	model.composer.SetValue("hello")
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := next.(*Model).composer.Value(); got != "" {
		t.Fatalf("non-realTerminal Enter should submit even if desktop Shift is held, got %q", got)
	}
}

func TestAltEnterAndCSIShiftEnterInsertNewline(t *testing.T) {
	// Alt+Enter inserts a newline cleanly.
	model := chatModel(t)
	model.composer.SetValue("line1")
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if got := next.(*Model).composer.Value(); got != "line1\n" {
		t.Fatalf("composer after Alt+Enter = %q, want %q", got, "line1\n")
	}

	// CSI u (\x1b[13;2u, \x1b[13;2:1u) and modifyOtherKeys (\x1b[27;2;13~) insert newlines.
	for _, seq := range [][]byte{
		[]byte("\x1b[13;2u"),
		[]byte("\x1b[13;2:1u"),
		[]byte("\x1b[27;2;13~"),
	} {
		m := chatModel(t)
		m.composer.SetValue("abc")
		n, _ := m.Update(fakeUnknownCSI(seq))
		if got := n.(*Model).composer.Value(); got != "abc\n" {
			t.Fatalf("composer after CSI %q = %q, want %q", seq, got, "abc\n")
		}
	}

	// Literal KeyMsg text matching the formatted CSI string must NOT be misclassified.
	literal := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?CSI[49 51 59 50 117]?")}
	if isShiftEnterCSI(literal) {
		t.Fatal("literal tea.KeyMsg must never match isShiftEnterCSI")
	}
}
