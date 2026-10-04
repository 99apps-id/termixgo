package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSplitMouseReportDoesNotClearComposer pins the leak fix: when an SGR
// mouse report is delivered across reads, bubbletea turns the leading ESC byte
// into a bare KeyEscape and the body into rune bursts. A lone Escape that is
// really the report must not clear the draft, and the fragments must not leak.
func TestSplitMouseReportDoesNotClearComposer(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("draft")

	// The realistic split: ESC alone, then head, numeric middle, terminator.
	model, _ = send(t, model, tea.KeyMsg{Type: tea.KeyEscape})
	model, _ = send(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<35")})
	model, _ = send(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("106")})
	model, _ = send(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(";27M")})

	if got := model.composer.Value(); got != "draft" {
		t.Errorf("composer = %q, want the draft preserved and no leaked numbers", got)
	}
	if model.escPending {
		t.Errorf("the report must resolve the pending Escape")
	}
	if model.escapeNoiseActive {
		t.Errorf("the noise machine must not stay armed after the terminator")
	}
}

// TestSplitMouseReportDoesNotStopARun proves the same lone-ESC byte does not
// read as a stop shortcut while a turn is running.
func TestSplitMouseReportDoesNotStopARun(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.composer.SetValue("draft")

	model, _ = send(t, model, tea.KeyMsg{Type: tea.KeyEscape})
	model, _ = send(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<35;106;27M")})

	if model.notice != "" {
		t.Errorf("a scroll during a run must not stop it, got notice %q", model.notice)
	}
	if got := model.composer.Value(); got != "draft" {
		t.Errorf("composer = %q, want the draft preserved", got)
	}
}

// TestGenuineEscapeStillSettles pins the other half of the trade: a real
// Escape with nothing after it still acts once the settle window closes.
func TestGenuineEscapeStillSettles(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("half a thought")

	held, _ := send(t, model, tea.KeyMsg{Type: tea.KeyEscape})
	if !held.escPending {
		t.Fatalf("a lone Escape must be held for settlement")
	}
	if held.composer.Value() != "half a thought" {
		t.Errorf("the draft must survive until the Escape is resolved")
	}

	settled, _ := send(t, held, settleEscMsg{})
	if settled.composer.Value() != "" {
		t.Errorf("a genuine Escape must clear the composer once settled, got %q", settled.composer.Value())
	}
}