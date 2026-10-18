package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

// wholeEscape matches one complete SGR sequence. Anything escape-shaped left
// after removing every whole sequence is a sliced one, and a terminal that
// meets a sliced sequence swallows the characters after it looking for the end.
var wholeEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

// malformedEscape reports whether a rendered view holds a broken sequence.
func malformedEscape(text string) bool {
	return strings.Contains(wholeEscape.ReplaceAllString(text, ""), "\x1b")
}

// widestLine returns the widest rendered line after colour is stripped.
//
// A line wider than the terminal is what makes the screen look garbled: the
// terminal wraps it, every row below shifts down, and the composer drifts off
// the frame.
func widestLine(text string) int {
	widest := 0
	for _, line := range strings.Split(stripANSI(text), "\n") {
		if width := len([]rune(line)); width > widest {
			widest = width
		}
	}
	return widest
}

// busyModel builds a chat model at a size with a wrapped user message and a
// long assistant answer, so every transcript renderer is exercised.
func busyModel(t *testing.T, width, height int) *Model {
	t.Helper()
	model := chatModel(t)
	resize(model, width, height)
	model.blocks = append(model.blocks, block{kind: blockUser, text: strings.Repeat("word ", 40)})
	model.applyEvent(agent.Event{Kind: agent.EventText, Text: strings.Repeat("chatter ", 60)})
	model.refresh()
	return model
}

// TestFrameNeverExceedsTheTerminalWidth is the layout contract: no rendered
// line may be wider than the terminal, at any common size and with a long
// message on screen. Violating it wraps the frame and pushes the composer down.
func TestFrameNeverExceedsTheTerminalWidth(t *testing.T) {
	for _, size := range []struct{ width, height int }{
		{120, 40}, {100, 30}, {80, 24}, {60, 18}, {50, 16},
	} {
		model := busyModel(t, size.width, size.height)
		if widest := widestLine(model.View()); widest > size.width {
			t.Errorf("at %dx%d the widest line is %d columns, which wraps the frame", size.width, size.height, widest)
		}
	}
}

// TestComposerIsExactlyTheTerminalWidth pins the composer fix: the border is
// part of the textarea's own layout, so the box is exactly as wide as the
// terminal and a wrapped line cannot spill onto the next row.
func TestComposerIsExactlyTheTerminalWidth(t *testing.T) {
	for _, width := range []int{40, 60, 80, 100, 120} {
		model := chatModel(t)
		resize(model, width, 30)
		for _, line := range strings.Split(stripANSI(model.viewComposer()), "\n") {
			if got := len([]rune(line)); got != width {
				t.Fatalf("at %d columns a composer line is %d wide:\n%s", width, got, stripANSI(model.viewComposer()))
			}
		}
	}
}

// TestComposerPromptAppearsOnceAndIndentsWrappedLines keeps a long message from
// repeating the marker on every visual line, which is what pushed the text onto
// an extra row.
func TestComposerPromptAppearsOnceAndIndentsWrappedLines(t *testing.T) {
	model := chatModel(t)
	resize(model, 40, 30)
	model = press(t, model, "-")
	model = press(t, model, "x")

	rendered := stripANSI(model.viewComposer())
	if strings.Count(rendered, "> ") != 1 {
		t.Errorf("the prompt should appear once:\n%s", rendered)
	}
	if !strings.Contains(rendered, "> -x") {
		t.Errorf("the typed text should follow the prompt:\n%s", rendered)
	}
}

// TestHeaderAndStatusClipToTheWidth covers the two summary rows that used to
// carry a fixed width: a long workspace path or model label must be clipped,
// not wrapped.
func TestHeaderAndStatusClipToTheWidth(t *testing.T) {
	model := chatModel(t)
	resize(model, 50, 16)

	if widest := widestLine(model.viewHeader()); widest > model.width {
		t.Errorf("the header is %d columns at width %d", widest, model.width)
	}
	if widest := widestLine(model.viewStatus()); widest > model.width {
		t.Errorf("the status line is %d columns at width %d", widest, model.width)
	}
	if widest := widestLine(model.viewHints()); widest > model.width {
		t.Errorf("the hints are %d columns at width %d", widest, model.width)
	}
}

// TestNoViewLeavesAMalformedEscape is the scrambled-screen guard: every screen
// state, at every usable width, must hand the terminal only whole escape
// sequences and no line wider than the terminal.
//
// A sliced sequence is what makes text look shuffled even though the stored
// bytes are clean, and an over-wide line wraps and pushes the frame down.
func TestNoViewLeavesAMalformedEscape(t *testing.T) {
	forceColor(t)
	const sentence = "Saya cek dulu mana yang dimaksud dan apakah ada cara mengukurnya di mesin ini. " +
		"Backend: Tauri 2 + Rust (`src-tauri/`), PTY via `portable-pty`."

	for _, width := range []int{50, 51, 60, 80, 100, 120, 200} {
		model := chatModel(t)
		resize(model, width, 30)
		model.blocks = append(model.blocks,
			block{kind: blockUser, text: strings.Repeat("word ", 30)},
			block{kind: blockThinking, running: true, reasoning: sentence},
			block{kind: blockTool, running: true, toolName: "read_file", toolLabel: "Reading something/very/long/path/main.go"},
			block{kind: blockAssistant, text: sentence},
			block{kind: blockPlan, plan: []agent.Todo{{Title: strings.Repeat("task ", 10), Status: "in_progress"}}},
		)
		model.notice = strings.Repeat("notice ", 10)
		model.running = true
		model.composer.SetValue(strings.Repeat("composer ", 8))

		states := []string{"chat", "slash", "mention", "help"}
		for _, state := range states {
			view := ""
			switch state {
			case "chat":
				model.refresh()
				view = model.View()
			case "slash":
				model.composer.SetValue("/st")
				model.updateSlashMatches()
				view = model.View()
			case "mention":
				model.composer.SetValue("@main")
				model.updateMentionMatches()
				view = model.View()
			case "help":
				model.current = modeHelp
				view = model.View()
				model.current = modeChat
			}
			checkViewClean(t, state, width, view)
		}

		model.pendingApproval = &agent.ApprovalRequest{
			Tool: "edit", Risk: "edit", Detail: "Editing a/very/long/path/main.go/that/keeps/going/on",
			Diff: "--- a.go\n+++ a.go\n-" + strings.Repeat("x", 200) + "\n+" + strings.Repeat("y", 200),
		}
		checkViewClean(t, "approval", width, model.View())
	}
}

// TestMenuFitsTheTerminalWidth pins the menu fix: rows are clipped before the
// Menu padding is added, so the padded block is exactly the terminal width
// rather than two columns over it.
func TestMenuFitsTheTerminalWidth(t *testing.T) {
	forceColor(t)
	for _, width := range []int{50, 60, 80, 120} {
		model := chatModel(t)
		resize(model, width, 30)
		model.openSlashMenu()
		model.slashInput = "/st"
		model.refreshSlashMenu()
		if model.slashMatches == nil {
			t.Fatalf("/st matched no command, so the menu renderer is not exercised")
		}
		if widest := widestLine(model.viewSlashMenu()); widest > width {
			t.Errorf("the slash menu is %d columns at width %d", widest, width)
		}

		model.composer.SetValue("@main")
		model.updateMentionMatches()
		if widest := widestLine(model.viewMentionMenu()); widest > width {
			t.Errorf("the mention menu is %d columns at width %d", widest, width)
		}
	}
}

// checkViewClean fails when a rendered view would confuse the terminal.
func checkViewClean(t *testing.T, label string, width int, view string) {
	t.Helper()
	if malformedEscape(view) {
		t.Errorf("%s at %d columns left a sliced escape, which swallows text", label, width)
	}
	if widest := widestLine(view); widest > width {
		t.Errorf("%s at %d columns rendered a %d-wide line, which wraps", label, width, widest)
	}
}
