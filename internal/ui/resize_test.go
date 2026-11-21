package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A width change in the inline layout clears the screen at once and, when
// the width has settled, prints the transcript again at the new width. Only
// the last change of a drag redraws.
func TestInlineResizeReprintsAtTheNewWidth(t *testing.T) {
	model := inlineModel(t)
	model.blocks = append(model.blocks, block{kind: blockAssistant, text: strings.Repeat("word ", 60)})
	model.refresh()
	model.pendingPrint = nil
	if model.printed != len(model.blocks) {
		t.Fatalf("everything should be printed before the resize")
	}

	for _, width := range []int{110, 90, 70} {
		next, cmd := model.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		model = next.(*Model)
		if cmd == nil {
			t.Fatalf("a width change must clear and schedule a redraw")
		}
	}
	stale, cmd := model.Update(resizeSettledMsg{seq: model.resizeSeq - 1})
	if cmd != nil || stale.(*Model).printed != len(model.blocks) {
		t.Fatalf("an earlier size of the drag must not redraw")
	}

	next, cmd := model.Update(resizeSettledMsg{seq: model.resizeSeq})
	model = next.(*Model)
	if cmd == nil {
		t.Fatalf("the settled width should reprint the transcript")
	}
	if model.printed != len(model.blocks) {
		t.Fatalf("after the reprint every block counts as printed again")
	}
	for _, line := range strings.Split(stripANSI(model.View()), "\n") {
		if len([]rune(line)) >= 70 {
			t.Fatalf("a live line reaches the terminal edge at width 70: %q", line)
		}
	}
}

// Growing the window clears too: Windows Terminal unwraps the old frame,
// which would otherwise make Bubble Tea erase printed lines.
func TestInlineGrowingAlsoClears(t *testing.T) {
	model := inlineModel(t)
	_, cmd := model.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	if cmd == nil {
		t.Fatalf("widening must clear and schedule a redraw as well")
	}
}

func TestImagePasteKeyNamesAKeyThatReachesTheProgram(t *testing.T) {
	model := chatModel(t)
	if hints := stripANSI(model.viewHints()); !strings.Contains(hints, imagePasteKey()+" image") {
		t.Fatalf("the hint row should name the image paste key:\n%s", hints)
	}
}
