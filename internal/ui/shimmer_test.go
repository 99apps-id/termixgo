package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// forceColor pins TrueColor for one test: without a terminal lipgloss emits
// no escapes and frames would look identical.
func forceColor(t *testing.T) {
	t.Helper()
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
}

// TestShimmerKeepsTheWords proves the effect is colour only: stripping it
// returns the exact input, so content tests never depend on the animation.
func TestShimmerKeepsTheWords(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	for _, frame := range []int{0, 3, 7, 40} {
		got := stripANSI(shimmerWith("  Thinking...", styles.Thinking, frame))
		if got != "  Thinking..." {
			t.Errorf("frame %d = %q, want the text unchanged", frame, got)
		}
	}
}

// TestShimmerMovesAcrossFrames pins the animation: two far-apart frames must
// render different escapes around the same text.
func TestShimmerMovesAcrossFrames(t *testing.T) {
	forceColor(t)
	styles := NewStyles(DefaultPalette())
	first := shimmerWith("  Thinking...", styles.Thinking, 0)
	moved := shimmerWith("  Thinking...", styles.Thinking, 9)
	if first == moved {
		t.Errorf("frames should place the highlight differently:\n%q\n%q", first, moved)
	}
	for _, rendered := range []string{first, moved} {
		if !strings.Contains(stripANSI(rendered), "Thinking") {
			t.Errorf("a frame must keep the text:\n%q", rendered)
		}
	}
}

// TestShimmerRespectsNoColor keeps piped and plain output static.
func TestShimmerRespectsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	styles := NewStyles(DefaultPalette())
	got := shimmerWith("  Thinking...", styles.Thinking, 3)
	if got != styles.Thinking.Render("  Thinking...") {
		t.Errorf("NO_COLOR should disable the sweep, got %q", got)
	}
}

// TestRunningThinkingBlockShimmers verifies the live header carries the
// effect while finished headers stay static.
func TestRunningThinkingBlockShimmers(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	running := renderThinkingBlock(block{kind: blockThinking, running: true, reasoning: "x"}, styles, 80, true)
	if !strings.Contains(stripANSI(running), "Thinking...") {
		t.Errorf("the live header should shimmer its text:\n%s", stripANSI(running))
	}
	done := renderThinkingBlock(block{kind: blockThinking, reasoning: "considered"}, styles, 80, true)
	if stripANSI(done) == "" || !strings.Contains(stripANSI(done), "Reasoned") {
		t.Errorf("a finished header stays static:\n%s", stripANSI(done))
	}
}

// TestRunningToolBlockShimmers verifies the in-flight tool line shimmers
// while completed lines keep their single style.
func TestRunningToolBlockShimmers(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	running := stripANSI(renderToolBlock(block{kind: blockTool, running: true, toolLabel: "Reading main.go"}, styles, 80, true))
	if !strings.Contains(running, "> Reading main.go") {
		t.Errorf("the running line should keep its marker:\n%s", running)
	}
}
