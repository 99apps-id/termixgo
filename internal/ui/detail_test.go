package ui

import (
	"strings"
	"testing"
)

// detailFixture builds a transcript with conversation plus working noise:
// reasoning, a tool process line and the answer.
func detailFixture(model *Model) *Model {
	model.blocks = append(model.blocks,
		block{kind: blockUser, text: "fix it"},
		block{kind: blockThinking, reasoning: "secret reasoning"},
		block{kind: blockTool, toolLabel: "Read secret.go", toolOK: true, toolMillis: 12},
		block{kind: blockAssistant, text: "done"},
	)
	return model
}

// TestCtrlOTogglesTheWorkingNoise is the detail contract: Ctrl+O hides the
// thinking, reasoning and tool process lines while keeping the conversation,
// and a second press brings them back.
func TestCtrlOTogglesTheWorkingNoise(t *testing.T) {
	model := detailFixture(chatModel(t))

	if view := display(model); !strings.Contains(view, "secret reasoning") {
		t.Fatalf("details should be visible at first:\n%s", view)
	}

	hidden := press(t, model, "ctrl+o")
	if hidden.showDetails {
		t.Errorf("showDetails should be false after Ctrl+O")
	}
	if view := display(hidden); strings.Contains(view, "secret reasoning") {
		t.Errorf("thinking and tool lines should be hidden:\n%s", view)
	}
	if view := display(hidden); !strings.Contains(view, "fix it") || !strings.Contains(view, "done") {
		t.Errorf("the conversation must stay visible:\n%s", view)
	}
	if hidden.notice == "" {
		t.Errorf("the toggle should explain itself in the status line")
	}

	shown := press(t, hidden, "ctrl+o")
	if !shown.showDetails {
		t.Errorf("showDetails should be true after the second Ctrl+O")
	}
	if view := display(shown); !strings.Contains(view, "secret reasoning") {
		t.Errorf("the details should be back:\n%s", view)
	}
}

// TestTranscriptLeavesABlankLineBetweenTasks pins the spacing: blocks are
// separated by an empty line so one task never runs into the next.
func TestTranscriptLeavesABlankLineBetweenTasks(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	rendered := stripANSI(transcript([]block{
		{kind: blockUser, text: "first task"},
		{kind: blockUser, text: "second task"},
	}, styles, 60, true))
	if !strings.Contains(rendered, "> first task\n\n> second task") {
		t.Errorf("tasks should be separated by a blank line:\n%s", rendered)
	}
}

// TestEnterAcceptsTheSlashCompletion fixes the slash discovery trap: with
// "/stat" highlighted as /status, Enter must run the command instead of
// reporting an unknown one.
func TestEnterAcceptsTheSlashCompletion(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("/stat")
	model.updateSlashMatches()

	submitted := press(t, model, "enter")
	view := display(submitted)
	if strings.Contains(view, "Unknown command") {
		t.Errorf("Enter should accept the highlighted command:\n%s", view)
	}
	if !strings.Contains(view, "workspace:") {
		t.Errorf("Enter should have run /status:\n%s", view)
	}
}

// TestEnterKeepsExplicitSlashText pins the other side: arguments pass
// through untouched, so "/cost extra" is not rewritten to a trigger.
func TestEnterKeepsExplicitSlashText(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("/cost extra")
	model.updateSlashMatches()

	submitted := press(t, model, "enter")
	if view := display(submitted); strings.Contains(view, "Unknown command") {
		t.Errorf("text with arguments must pass through:\n%s", view)
	}
}

// TestToolBlockKeepsTimingWhenClipped pins the garble fix: clipping a long
// label must never slice the timing suffix mid-escape, so it stays whole.
func TestToolBlockKeepsTimingWhenClipped(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	rendered := stripANSI(renderToolBlock(block{
		kind:       blockTool,
		toolOK:     true,
		toolLabel:  strings.Repeat("p", 200),
		toolMillis: 42,
	}, styles, 40, true))
	if !strings.Contains(rendered, "(42ms)") {
		t.Errorf("the clipped line should keep its timing:\n%s", rendered)
	}
	for _, line := range strings.Split(rendered, "\n") {
		if len([]rune(line)) > 40 {
			t.Errorf("line exceeds the width: %q", line)
		}
	}
}
