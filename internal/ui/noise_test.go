package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestScrubComposerRemovesLeakedMouseReports pins the backstop: whatever path
// delivered a mouse report, the composer draft ends clean.
func TestScrubComposerRemovesLeakedMouseReports(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("draft [<65;75;20M[<35;75;20M 1;15M end")
	model.scrubComposer()
	if value := model.composer.Value(); strings.Contains(value, "[<") || strings.Contains(value, "1;15M") {
		t.Errorf("composer still has noise: %q", value)
	}

	model.composer.SetValue("keep \x1b[<35;63;32M this")
	model.scrubComposer()
	if value := model.composer.Value(); strings.Contains(value, "[<") || !strings.Contains(value, "keep") || !strings.Contains(value, "this") {
		t.Errorf("composer = %q, want the text with the escape removed", value)
	}
}

// TestComposerDropsMouseFragments pins the leak fix: SGR mouse reports that
// bubbletea split into key fragments (a wheel report, a head without its final
// byte, a bare tail, a truncated triple, and a piece still carrying the ESC)
// must never reach the composer, while real typing survives.
func TestComposerDropsMouseFragments(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("draft")

	noise := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'['}, Alt: true},
		{Type: tea.KeyRunes, Runes: []rune("<35;106;27")},
		{Type: tea.KeyRunes, Runes: []rune(";27M")},
		{Type: tea.KeyRunes, Runes: []rune("[<65;81;15M")},
		{Type: tea.KeyRunes, Runes: []rune("1;15M")},
		{Type: tea.KeyRunes, Runes: []rune(";107;28M")},
		{Type: tea.KeyRunes, Runes: []rune("\x1b[<35;63;32M")},
	}
	current := model
	for _, key := range noise {
		current, _ = send(t, current, key)
	}
	if got := current.composer.Value(); got != "draft" {
		t.Errorf("mouse fragments leaked into the composer: %q", got)
	}

	// A batch of mouse noise that arrives as one paste is dropped too.
	next, _ := send(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<35;60;19M[<35;60;18M20M"), Paste: true})
	if got := next.composer.Value(); got != "draft" {
		t.Errorf("a mouse-noise paste leaked: %q", got)
	}
	current = next

	// Real typing survives, including the characters a coder uses.
	for _, typed := range []string{"<", "=", ">", "<=", "<<", "->", "a<b", "123", "M", "[", "1;2;3"} {
		step, _ := send(t, current, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(typed)})
		current = step
	}
	want := "draft" + "<=><=<<->a<b123M[1;2;3"
	if got := current.composer.Value(); got != want {
		t.Errorf("composer = %q, want %q (a printable key was muted)", got, want)
	}
}
