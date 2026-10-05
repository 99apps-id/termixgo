package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestConsoleOneRuneReportDoesNotClearTheDraft pins the Windows Terminal shape.
//
// Windows Terminal delivers mouse input as virtual terminal sequences, and
// bubbletea's console reader (readConInputs) hands back one rune per KeyMsg. So
// an SGR report arrives as ESC, then "[", "<", "3", "5", ";", ... one message
// at a time. Before the fix the "[" was not recognised as a follow-on (it
// carries no digit), so the pending Escape settled and cleared the draft on
// every scroll.
func TestConsoleOneRuneReportDoesNotClearTheDraft(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("draft")

	held, _ := send(t, model, tea.KeyMsg{Type: tea.KeyEscape})
	if !held.escPending {
		t.Fatalf("a lone Escape must be held for settlement")
	}

	for _, r := range "[<35;106;27M" {
		held, _ = send(t, held, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if got := held.composer.Value(); got != "draft" {
		t.Errorf("composer = %q, want the draft preserved", got)
	}
	if held.escPending {
		t.Errorf("the report must resolve the pending Escape")
	}
	if held.escapeNoiseActive {
		t.Errorf("the noise machine must not stay armed after the terminator")
	}
}

// TestConsoleOneRuneReportPreservesTypingAfterwards proves the noise machine
// disarms at the terminator, so the next real keystroke is not swallowed.
func TestConsoleOneRuneReportPreservesTypingAfterwards(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("draft")

	held, _ := send(t, model, tea.KeyMsg{Type: tea.KeyEscape})
	for _, r := range "[<35;106;27M" {
		held, _ = send(t, held, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	held, _ = send(t, held, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	if got := held.composer.Value(); got != "draftx" {
		t.Errorf("composer = %q, want the draft plus the typed rune", got)
	}
}

// TestGenuineEscapeThenTypingStillActs pins the other half: accepting a bare
// "[" as a report introducer must not stop a real Escape from acting. Escape
// followed by an ordinary character still settles and clears the draft, and the
// character is typed.
func TestGenuineEscapeThenTypingStillActs(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("draft")

	model, _ = send(t, model, tea.KeyMsg{Type: tea.KeyEscape})
	model, _ = send(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})

	if model.escPending {
		t.Errorf("an ordinary key must resolve the pending Escape")
	}
	if got := model.composer.Value(); got != "h" {
		t.Errorf("composer = %q, want the escaped draft replaced by the typed rune", got)
	}
}
