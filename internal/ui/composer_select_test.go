package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestComposerSelectionTextExtractsPartial proves a composer selection reads
// the selected columns out of the rendered box, past the border and prompt.
func TestComposerSelectionTextExtractsPartial(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("hello world")
	model.composerSelActive = true

	model.composerSelAnchor = selPoint{line: 0, col: 0}
	model.composerSelFocus = selPoint{line: 0, col: 5}
	if got := model.composerSelectionText(); got != "hello" {
		t.Errorf("partial selection = %q, want %q", got, "hello")
	}

	model.composerSelAnchor = selPoint{line: 0, col: 6}
	model.composerSelFocus = selPoint{line: 0, col: 100}
	if got := model.composerSelectionText(); got != "world" {
		t.Errorf("tail selection = %q, want %q", got, "world")
	}
}

// TestComposerDragSelectsAndCopies drives the gesture: a press in the composer
// begins a selection, motion extends it, and release copies it.
func TestComposerDragSelectsAndCopies(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("hello world")

	top := model.composerTopRow()
	start := composerContentLeft
	afterPress, _ := send(t, model, tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: start, Y: top})
	if !afterPress.composerSelActive {
		t.Fatal("a press in the composer must begin a selection")
	}

	afterMotion, _ := send(t, afterPress, tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: start + 5, Y: top})
	if got := afterMotion.composerSelectionText(); got != "hello" {
		t.Errorf("drag selection = %q, want %q", got, "hello")
	}

	afterRelease, cmd := send(t, afterMotion, tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: start + 5, Y: top})
	if cmd == nil {
		t.Error("release must copy the composer selection")
	}
	if afterRelease.composerSelActive {
		t.Error("the highlight should clear after the copy")
	}
	if !strings.Contains(afterRelease.notice, "Copied 5 characters") {
		t.Errorf("notice = %q, want a five-character copy", afterRelease.notice)
	}
}
