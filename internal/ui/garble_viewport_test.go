package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"

	"github.com/99apps-id/termixgo/internal/agent"
)

// realGarbleText is the stored assistant message once seen mangled on
// screen: dropped spaces and backticks plus shuffled letters, while storage
// held clean bytes.
const realGarbleText = "Backend: Tauri 2 + Rust (`src-tauri/`), PTY via `portable-pty`."

// TestViewportPreservesContent renders styled assistant text through a real
// viewport exactly like production View does, and requires the visible words
// to match the input words in order.
func TestViewportPreservesContent(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	for _, width := range []int{80, 100, 120, 160} {
		body := renderMarkdown(realGarbleText, styles, width-2)
		indented := indent(body, "  ")

		viewportModel := viewport.New(width, 30)
		viewportModel.SetContent(indented)
		viewportModel.GotoBottom()
		visible := stripANSI(viewportModel.View())

		want := strings.Fields(strings.ReplaceAll(realGarbleText, "`", ""))
		got := strings.Fields(visible)
		if len(got) != len(want) {
			t.Errorf("width %d: %d words, want %d:\n%q", width, len(got), len(want), visible)
			continue
		}
		for index := range want {
			if got[index] != want[index] {
				t.Errorf("width %d word %d = %q, want %q", width, index, got[index], want[index])
			}
		}
	}
}

// TestStreamThrottleSkipsBursts proves text pacing: a delta arriving right
// after a paint updates state but holds the pixels, so a fast model cannot
// flood the terminal with full frames.
func TestStreamThrottleSkipsBursts(t *testing.T) {
	model := chatModel(t)
	painted, _ := send(t, model, eventMsg(agent.Event{Kind: agent.EventText, Text: "first"}))
	painted.lastPaint = time.Now()
	held, _ := send(t, painted, eventMsg(agent.Event{Kind: agent.EventText, Text: " second"}))

	// State applied, pixels held: the block has both words, the viewport
	// still shows only the first paint.
	var blockText string
	for _, item := range held.blocks {
		if item.kind == blockAssistant {
			blockText += item.text
		}
	}
	if blockText != "first second" {
		t.Fatalf("state must apply at once, got %q", blockText)
	}
	if strings.Contains(stripANSI(held.viewport.View()), "second") {
		t.Errorf("a burst delta should hold its paint")
	}
	if view := display(held); !strings.Contains(view, "first second") {
		t.Errorf("an explicit refresh must catch up:\n%s", view)
	}
}

// TestStructuralEventPaintsAtOnce proves boundaries never lag: a tool start
// right after held text forces the paint carrying both.
func TestStructuralEventPaintsAtOnce(t *testing.T) {
	model := chatModel(t)
	painted, _ := send(t, model, eventMsg(agent.Event{Kind: agent.EventText, Text: "before tool"}))
	painted.lastPaint = time.Now()
	held, _ := send(t, painted, eventMsg(agent.Event{Kind: agent.EventText, Text: " held"}))
	moved, _ := send(t, held, eventMsg(agent.Event{Kind: agent.EventToolStart, ToolName: "read_file", ToolLabel: "Reading main.go"}))
	if view := stripANSI(moved.viewport.View()); !strings.Contains(view, "before tool held") {
		t.Errorf("a structural event must flush held text:\n%s", view)
	}
}

// composer and hints exactly like View and requires the assistant sentence
// to survive the join.
func TestFullViewPreservesAssistantText(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	const width = 120
	body := indent(renderMarkdown(realGarbleText, styles, width-2), "  ")
	viewportModel := viewport.New(width, 20)
	viewportModel.SetContent(body)
	viewportModel.GotoBottom()
	joined := lipgloss.JoinVertical(lipgloss.Left,
		"Termixgo header",
		viewportModel.View(),
		"status line",
		"> composer",
		"hints",
	)
	visible := stripANSI(joined)
	// Code spans render without their backticks by design; only order and
	// completeness of the remaining words matter here.
	plain := strings.ReplaceAll(realGarbleText, "`", "")
	for _, word := range strings.Fields(plain) {
		if !strings.Contains(visible, word) {
			t.Errorf("joined view lost %q:\n%s", word, visible)
		}
	}
}
