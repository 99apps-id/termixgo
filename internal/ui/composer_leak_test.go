package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The fragments below are what bubbletea v1.3.10 hands to Update when one SGR
// mouse report arrives split across reads. readAnsiInputs only rejoins a read
// that fills its 256 byte buffer (canHaveMoreData), so on a short read the
// parser treats the cut as an event boundary. detectOneMsg then decodes the
// leading ESC alone as Escape, or ESC plus one rune as Alt+that rune, and
// leaves the remaining parameter bytes as a plain KeyRunes burst. A fragment
// therefore reaches the composer as one of: a bare Escape, Alt+[, or runes
// made only of digits, ';', '<' and an optional M/m terminator.
//
// This table drives those shapes through the real Update path. A split report
// must leave the draft exactly as it was; real typing must survive whole.
func TestSplitMouseReportFragmentsNeverReachTheComposer(t *testing.T) {
	runeKey := func(s string) tea.KeyMsg {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	altBracket := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}, Alt: true}
	esc := tea.KeyMsg{Type: tea.KeyEscape}

	cases := []struct {
		name string
		keys []tea.KeyMsg
		want string
	}{
		// Report \x1b[<35;106;27M cut at every boundary.
		{"cut after ESC", []tea.KeyMsg{esc, runeKey("[<35;106;27M")}, "draft"},
		{"cut after ESC[", []tea.KeyMsg{altBracket, runeKey("<35;106;27M")}, "draft"},
		{"cut after ESC[<", []tea.KeyMsg{altBracket, runeKey("<"), runeKey("35;106;27M")}, "draft"},
		{"cut after ESC[<3", []tea.KeyMsg{altBracket, runeKey("35"), runeKey(";106;27M")}, "draft"},
		{"cut after ESC[<35", []tea.KeyMsg{altBracket, runeKey("35"), runeKey(";106;27M")}, "draft"},
		{"head without semicolon as one burst", []tea.KeyMsg{runeKey("[<35")}, "draft"},
		{"head without semicolon then tail", []tea.KeyMsg{runeKey("[<35"), runeKey(";106;27M")}, "draft"},
		{"alternate dot report head", []tea.KeyMsg{runeKey("[<")}, "draft"},
		{"parameter tail only", []tea.KeyMsg{altBracket, runeKey("35;106;27")}, "draft"},
		{"digit tail after armed head", []tea.KeyMsg{altBracket, runeKey("35")}, "draft"},
		{"terminated chunk without ESC", []tea.KeyMsg{runeKey(";106;27M")}, "draft"},
		{"short terminated chunk", []tea.KeyMsg{runeKey("27M")}, "draft"},

		// Real typing, including the punctuation a coder uses.
		{"typed digits", []tea.KeyMsg{runeKey("123")}, "draft123"},
		{"typed semicolon list", []tea.KeyMsg{runeKey("1;2;3")}, "draft1;2;3"},
		{"typed angle comparison", []tea.KeyMsg{runeKey("a<b")}, "drafta<b"},
		{"typed shift comma", []tea.KeyMsg{runeKey("x<y;z")}, "draftx<y;z"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := chatModel(t)
			model.composer.SetValue("draft")
			current := model
			for _, key := range tc.keys {
				step, _ := send(t, current, key)
				current = step
			}
			if got := current.composer.Value(); got != tc.want {
				t.Errorf("composer = %q, want %q", got, tc.want)
			}
		})
	}
}
