package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// TestInsertHighlightWrapsColumns keeps the highlight honest: the text is
// unchanged and only the two boundaries gain a reverse-video toggle, so the
// colours already in the line survive.
func TestInsertHighlightWrapsColumns(t *testing.T) {
	line := "\x1b[32mhello world\x1b[0m"
	out := insertHighlight(line, 6, 11)
	if plain := ansi.Strip(out); plain != "hello world" {
		t.Errorf("strip = %q, want the text unchanged", plain)
	}
	if !strings.HasPrefix(out, "\x1b[32mhello ") || !strings.Contains(out, "\x1b[7mworld") {
		t.Errorf("reverse video must open at the selection start: %q", out)
	}
	if !strings.Contains(out, "\x1b[27m") {
		t.Errorf("reverse video must close after the selection: %q", out)
	}
}

// TestSelectionTextCutsColumnsAndStrips proves the copy payload: only the
// selected columns, styling removed, each line kept on its own row.
func TestSelectionTextCutsColumnsAndStrips(t *testing.T) {
	lines := []string{"\x1b[32mhello world\x1b[0m", "second line"}
	got := selectionText(lines, selPoint{line: 0, col: 6}, selPoint{line: 1, col: 6})
	if got != "world\nsecond" {
		t.Errorf("selection = %q, want %q", got, "world\nsecond")
	}
}

// TestMouseDragSelectsAndCopies drives the whole gesture: press begins a
// selection, motion extends it, and release copies it and keeps the highlight.
func TestMouseDragSelectsAndCopies(t *testing.T) {
	model := scrollFixture(chatModel(t))
	top := model.transcriptTop()

	afterPress, _ := send(t, model, tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: top})
	if !afterPress.selecting {
		t.Fatal("a left press over the transcript must begin a selection")
	}
	anchor := afterPress.selAnchor

	afterMotion, _ := send(t, afterPress, tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 6, Y: top + 2})
	if afterMotion.selFocus.line != anchor.line+2 {
		t.Fatalf("drag should extend two rows, got %+v from %+v", afterMotion.selFocus, anchor)
	}

	afterRelease, cmd := send(t, afterMotion, tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 6, Y: top + 2})
	if cmd == nil {
		t.Error("release must copy the selection")
	}
	if !afterRelease.selActive {
		t.Error("the highlight should stay after release")
	}
	if !strings.Contains(afterRelease.notice, "Copied") {
		t.Errorf("notice = %q, want a copy confirmation", afterRelease.notice)
	}
}

// TestComposerSelectAllCopyAndDelete walks the composer selection: Ctrl+A
// selects all, Ctrl+C copies it without quitting, and the next keystroke
// consumes the selection.
func TestComposerSelectAllCopyAndDelete(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("some draft text")

	selected, _ := send(t, model, tea.KeyMsg{Type: tea.KeyCtrlA})
	if !selected.composerSelected {
		t.Fatal("Ctrl+A must select all composer text")
	}
	if got := selected.composer.Value(); got != "some draft text" {
		t.Errorf("Ctrl+A must not change the text, got %q", got)
	}

	copied, cmd := send(t, selected, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("Ctrl+C on a selection must copy")
	}
	if copied.composerSelected {
		t.Error("copy must clear the selection")
	}
	if !strings.Contains(copied.notice, "Copied") {
		t.Errorf("notice = %q, want a copy confirmation", copied.notice)
	}

	again, _ := send(t, copied, tea.KeyMsg{Type: tea.KeyCtrlA})
	deleted, _ := send(t, again, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := deleted.composer.Value(); got != "" {
		t.Errorf("Backspace on a full selection must clear the composer, got %q", got)
	}
	if deleted.composerSelected {
		t.Error("the selection must clear after delete")
	}

	// Typing after select-all replaces the text instead of appending to it.
	fresh := chatModel(t)
	fresh.composer.SetValue("old")
	sel, _ := send(t, fresh, tea.KeyMsg{Type: tea.KeyCtrlA})
	typed, _ := send(t, sel, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("new")})
	if got := typed.composer.Value(); got != "new" {
		t.Errorf("typing over a selection = %q, want %q", got, "new")
	}
}

// TestCtrlXQuitsAndCtrlCDoesNot proves the split: Ctrl+X is the quit and Ctrl+C
// only copies, so an operator copying with an empty composer never closes the
// program.
func TestCtrlXQuitsAndCtrlCDoesNot(t *testing.T) {
	model := chatModel(t)

	_, quit := send(t, model, tea.KeyMsg{Type: tea.KeyCtrlX})
	if quit == nil {
		t.Fatal("Ctrl+X must quit")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Errorf("Ctrl+X should produce tea.QuitMsg, got %T", quit())
	}

	if _, copyCmd := send(t, model, tea.KeyMsg{Type: tea.KeyCtrlC}); copyCmd != nil {
		t.Errorf("Ctrl+C with nothing selected must not produce a command, got %T", copyCmd)
	}
}

// TestScrollByEasesTowardTheTarget proves the animation: a downward notch
// schedules a frame and that frame advances the offset without overshooting.
func TestScrollByEasesTowardTheTarget(t *testing.T) {
	model := scrollFixture(chatModel(t))
	model.viewport.SetYOffset(0)
	model.scrollTarget = 0

	cmd := model.scrollBy(wheelStepLines)
	if cmd == nil {
		t.Fatal("a downward wheel must schedule the smooth-scroll frame")
	}
	before := model.viewport.YOffset
	step, _ := model.Update(scrollTickMsg{})
	moved := step.(*Model)
	if moved.viewport.YOffset <= before {
		t.Errorf("a scroll frame must advance the offset, %d did not exceed %d", moved.viewport.YOffset, before)
	}
	if limit := moved.maxScroll(); moved.scrollTarget > limit {
		t.Errorf("target %d exceeds the maximum offset %d", moved.scrollTarget, limit)
	}
}

// TestScrollStopsWhenTheContentShrinks pins the guard against a spin: a target
// set before the transcript was cleared must be clamped so the tick loop can
// reach it, instead of chasing an offset the viewport keeps clamping.
func TestScrollStopsWhenTheContentShrinks(t *testing.T) {
	model := scrollFixture(chatModel(t))
	model.scrollTarget = 10_000
	model.scrollTicking = true

	// /new empties the transcript under the animation.
	model.contentLines = nil
	model.viewport.SetContent("")
	model.stepScroll()

	if model.scrollTarget != model.maxScroll() {
		t.Fatalf("stale target %d not clamped to the maximum %d", model.scrollTarget, model.maxScroll())
	}
	_, cmd := model.Update(scrollTickMsg{})
	if cmd != nil {
		t.Errorf("the animation must stop once the target is reachable")
	}
	if model.scrollTicking {
		t.Errorf("scrollTicking must be cleared when the target is reached")
	}
}
