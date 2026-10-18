package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"

	"github.com/99apps-id/termixgo/internal/agent"
)

// escapePattern matches one complete SGR sequence. Anything that looks like
// an escape but fails this pattern is a malformed sequence, and terminals
// eat the characters after one looking for its end.
var escapePattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

// TestStyledOutputHasOnlyWholeEscapes forces colour on and requires every
// escape in a fully styled transcript to be well-formed. A sliced sequence
// makes the terminal swallow following text, which reads exactly like the
// dropped characters seen on screen.
func TestStyledOutputHasOnlyWholeEscapes(t *testing.T) {
	forceColor(t)
	styles := NewStyles(DefaultPalette())
	text := "Backend: Tauri 2 + Rust (`src-tauri/`), PTY via `portable-pty`.\n\nSecond paragraph with **bold** and `code` and a very long unbroken word https://example.com/some/deep/path/that/never/breaks/at/all/ok."
	for _, width := range []int{40, 60, 80, 120, 200} {
		rendered := transcript([]block{{kind: blockAssistant, text: text}}, styles, width, true)
		stripped := escapePattern.ReplaceAllString(rendered, "")
		if strings.Contains(stripped, "\x1b") {
			t.Errorf("width %d holds a malformed escape:\n%q", width, rendered)
		}
		// Every character must survive with escapes present, in order. Code
		// spans and bold markers are consumed by styling by design, and a word
		// longer than the line is hard-split, so the comparison ignores
		// whitespace: the non-space character stream must match exactly.
		plain := strings.ReplaceAll(text, "`", "")
		plain = strings.ReplaceAll(plain, "**", "")
		want := strings.Join(strings.Fields(plain), "")
		got := strings.Join(strings.Fields(stripped), "")
		if got != want {
			t.Errorf("width %d: character stream changed:\n got %q\nwant %q", width, got, want)
		}
	}
}

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

// TestFullViewHasOnlyWholeEscapes renders a busy screen exactly like
// production View does: running thinking with shimmer, a running tool, an
// approval dialog with a diff, and a non-empty composer. Every escape must
// be well-formed, because the composer rewrap and the per-rune shimmer are
// the two places most likely to slice a sequence.
func TestFullViewHasOnlyWholeEscapes(t *testing.T) {
	forceColor(t)
	model := chatModel(t)
	resize(model, 120, 40)
	model.blocks = append(model.blocks,
		block{kind: blockUser, text: "fix it"},
		block{kind: blockThinking, running: true, reasoning: "live thought"},
		block{kind: blockTool, running: true, toolName: "read_file", toolLabel: "Reading main.go"},
		block{kind: blockAssistant, text: realGarbleText},
	)
	model.composer.SetValue("follow-up @mai")
	model.pendingApproval = &agent.ApprovalRequest{
		Tool: "edit", Risk: "edit", Detail: "Editing main.go",
		Diff: "--- main.go\n+++ main.go\n-func A() {}\n+func A() int {}",
	}
	model.refresh()
	view := model.View()
	stripped := escapePattern.ReplaceAllString(view, "")
	if strings.Contains(stripped, "\x1b") {
		t.Errorf("the full view holds a malformed escape:\n%q", view)
	}
	// The approval dialog replaces the composer row, so the composer text is
	// intentionally absent here; its rewrap is covered by composer tests.
	for _, word := range []string{"Thinking", "Reading", "Backend", "Approval needed"} {
		if !strings.Contains(stripped, word) {
			t.Errorf("the full view lost %q", word)
		}
	}
}
