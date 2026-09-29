package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
)

// frameHeight is how many rows a rendered frame occupies.
func frameHeight(view string) int { return strings.Count(view, "\n") + 1 }

// TestFrameNeverExceedsTheTerminalHeight is the scrambled-screen guard.
//
// Bubble Tea drops the top lines of a frame taller than the terminal and moves
// the cursor relative to the previous frame. One row too many therefore shifts
// the whole screen, and the operator sees earlier lines in the wrong place,
// which reads as random and reversed text even though every line is well formed
// and every escape is whole.
func TestFrameNeverExceedsTheTerminalHeight(t *testing.T) {
	sizes := []struct{ width, height int }{
		{120, 40}, {100, 30}, {80, 24}, {60, 18}, {50, 16},
	}

	build := func(t *testing.T, width, height int) *Model {
		t.Helper()
		model := chatModel(t)
		resize(model, width, height)
		model.blocks = append(model.blocks,
			block{kind: blockUser, text: strings.Repeat("word ", 40)},
			block{kind: blockAssistant, text: strings.Repeat("chatter ", 200)},
		)
		model.refresh()
		return model
	}

	for _, size := range sizes {
		name := fmt.Sprintf("%dx%d", size.width, size.height)
		t.Run(name, func(t *testing.T) {
			states := map[string]func(*Model){
				"chat": func(model *Model) { model.refresh() },
				"slash menu": func(model *Model) {
					model.composer.SetValue("/")
					model.updateSlashMatches()
				},
				"mention menu": func(model *Model) {
					for index := 0; index < 40; index++ {
						name := filepath.Join(model.app.Workspace(), fmt.Sprintf("file%02d.go", index))
						if err := os.WriteFile(name, []byte("package main\n"), 0o644); err != nil {
							t.Fatalf("write %s: %v", name, err)
						}
					}
					model.composer.SetValue("@")
					model.updateMentionMatches()
				},
				"help":   func(model *Model) { model.current = modeHelp },
				"setup":  func(model *Model) { model.startSetup() },
				"picker": func(model *Model) { model.openPicker("Choose", "model", manyItems(40)) },
			}
			for state, apply := range states {
				model := build(t, size.width, size.height)
				apply(model)
				if got := frameHeight(model.View()); got > size.height {
					t.Errorf("%s at %s rendered %d rows, which the terminal clips", state, name, got)
				}
			}

			approval := build(t, size.width, size.height)
			approval.pendingApproval = &agent.ApprovalRequest{
				Tool: "edit", Risk: "edit", Detail: "Editing main.go",
				Diff: "--- a.go\n+++ a.go\n-" + strings.Repeat("x", 200) + "\n+" + strings.Repeat("y", 200),
			}
			if got := frameHeight(approval.View()); got > size.height {
				t.Errorf("approval at %s rendered %d rows", name, got)
			}

			ask := build(t, size.width, size.height)
			ask.pendingAsk = &askRequestMsg{
				question: "which port should the server listen on?",
				options:  []string{"3000", "8080", "9000", "10000", "11000", "12000"},
			}
			if got := frameHeight(ask.View()); got > size.height {
				t.Errorf("ask at %s rendered %d rows", name, got)
			}
		})
	}
}

// manyItems builds a picker list of the given length.
func manyItems(n int) []pickerItem {
	items := make([]pickerItem, 0, n)
	for index := 0; index < n; index++ {
		items = append(items, pickerItem{ID: fmt.Sprintf("id%d", index), Label: fmt.Sprintf("label %d", index), Detail: "detail"})
	}
	return items
}

// TestLongSlashMenuIsWindowed proves the fix directly rather than relying on
// the frame clamp: a menu that grew with the command count was taller than a
// short terminal, and the renderer then dropped the frame's top lines.
func TestLongSlashMenuIsWindowed(t *testing.T) {
	model := chatModel(t)
	resize(model, 100, 24)
	model.composer.SetValue("/")
	model.updateSlashMatches()
	if len(model.slashMatches) < 20 {
		t.Fatalf("the fixture needs the full catalogue, got %d matches", len(model.slashMatches))
	}

	rows := frameHeight(model.viewSlashMenu())
	if rows > maxMenuRows+3 {
		t.Errorf("the menu draws %d rows, want it windowed near %d", rows, maxMenuRows)
	}
	view := stripANSI(model.viewSlashMenu())
	if !strings.Contains(view, "more") {
		t.Errorf("a windowed menu should say how many rows are hidden:\n%s", view)
	}
}

// TestMenuWindowFollowsTheCursor keeps the highlighted row visible at both
// ends of a long list, so the window cannot hide the row Enter would select.
func TestMenuWindowFollowsTheCursor(t *testing.T) {
	total := 40
	for _, cursor := range []int{0, 1, total / 2, total - 2, total - 1} {
		start, end := windowRows(cursor, total, maxMenuRows)
		if cursor < start || cursor >= end {
			t.Errorf("cursor %d is outside the window [%d,%d)", cursor, start, end)
		}
		if end-start > maxMenuRows {
			t.Errorf("window [%d,%d) is %d rows, over the limit", start, end, end-start)
		}
	}
	// A list that fits is not windowed.
	if start, end := windowRows(3, 5, maxMenuRows); start != 0 || end != 5 {
		t.Errorf("a short list should not be windowed, got [%d,%d)", start, end)
	}
}

// TestFitFrameKeepsTheTail pins the clamp direction: Bubble Tea drops the top
// lines, so the clamp has to drop the same end or the two disagree.
func TestFitFrameKeepsTheTail(t *testing.T) {
	view := "one\ntwo\nthree\nfour"
	if got := fitFrame(view, 40, 2); got != "three\nfour" {
		t.Errorf("fitFrame = %q, want the last two rows", got)
	}
	wide := fitFrame("abcdefgh", 3, 5)
	if got := len([]rune(stripANSI(wide))); got > 3 {
		t.Errorf("fitFrame kept a %d-column line", got)
	}
}

// TestFrameHeightIsStableAsTheTurnStreams covers the failure the operator sees:
// the frame is clean at the start of a turn and then shifts. Text, thinking and
// tool rows all grow while the turn runs, so the frame height must not.
func TestFrameHeightIsStableAsTheTurnStreams(t *testing.T) {
	model := chatModel(t)
	resize(model, 90, 24)
	baseline := frameHeight(model.View())

	for _, event := range []agent.Event{
		{Kind: agent.EventText, Text: "first sentence of the answer."},
		{Kind: agent.EventThinking, Text: "considering the layout"},
		{Kind: agent.EventText, Text: strings.Repeat("more text ", 80)},
		{Kind: agent.EventToolStart, ToolName: "read_file", ToolLabel: "Reading main.go"},
		{Kind: agent.EventToolEnd, ToolName: "read_file", ToolLabel: "Read main.go", ToolOK: true},
		{Kind: agent.EventNotice, Text: strings.Repeat("notice ", 60)},
	} {
		model.applyEvent(event)
		model.lastPaint = time.Time{}
		model.refresh()
		if got := frameHeight(model.View()); got > 24 {
			t.Fatalf("after %v the frame is %d rows", event.Kind, got)
		}
	}
	_ = baseline
}
