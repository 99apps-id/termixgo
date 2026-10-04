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

// TestComposerDropsMouseNoisePaste pins the bulk shape: a burst of mouse
// reports that arrives as one paste is dropped, while a real paste survives.
func TestComposerDropsMouseNoisePaste(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("draft")

	noise := "[<35;60;19M[<35;60;18M20M[<35;61;17M"
	next, _ := send(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(noise), Paste: true})
	if got := next.composer.Value(); got != "draft" {
		t.Errorf("a mouse-noise paste leaked: %q", got)
	}

	next2, _ := send(t, next, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("func main() { x := 3M }"), Paste: true})
	if got := next2.composer.Value(); !strings.Contains(got, "func main()") {
		t.Errorf("a real paste was dropped: %q", got)
	}
}

// TestComposerDropsControlCharacters pins the last leak shape: a fragment that
// still carries the ESC byte must never reach the composer.
func TestComposerDropsControlCharacters(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("draft")
	next, _ := send(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("\x1b[<35;106;27M")})
	if got := next.composer.Value(); got != "draft" {
		t.Errorf("an ESC-bearing fragment leaked: %q", got)
	}
}

// TestComposerKeepsCodingCharacters pins that the terminal-noise filter does
// not mute printable characters a coder types: '<' begins comparisons, shifts
// and template arguments.
func TestComposerKeepsCodingCharacters(t *testing.T) {
	model := chatModel(t)
	for _, text := range []string{"<", "=", ">", "<=", "<<", "->", "a<b"} {
		next, _ := send(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
		model = next
	}
	if want := "<=><=<<->a<b"; model.composer.Value() != want {
		t.Errorf("composer = %q, want %q (a printable key was muted as noise)", model.composer.Value(), want)
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
