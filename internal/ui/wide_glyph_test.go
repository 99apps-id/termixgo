package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// displayWidth is the widest rendered line measured in terminal columns.
//
// widestLine counts runes, which is exactly the mistake these tests guard
// against: a wide glyph is one rune but two columns, so a frame that looks
// narrow can still overflow and wrap.
func displayWidth(text string) int {
	widest := 0
	for _, line := range strings.Split(stripANSI(text), "\n") {
		if width := lipgloss.Width(line); width > widest {
			widest = width
		}
	}
	return widest
}

// wideText mixes CJK, a fullwidth bracket and an emoji so every wrapping path
// meets a two-column glyph.
const wideText = "日本語のテキストです これは長い行になります もう一度 日本語のテキストです " +
	"バグを直す 【重要】 🚀 emoji も あります そして まだまだ 続きます"

// TestAssistantWideGlyphsStayInsideTheFrame is the scrambled-screen guard for
// wide characters. Wrapping by rune count let a line measure short, overflow
// the terminal and wrap, which shifted every row below it.
func TestAssistantWideGlyphsStayInsideTheFrame(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	for _, width := range []int{40, 60, 80, 120} {
		rendered := transcript([]block{{kind: blockAssistant, text: wideText}}, styles, width, true)
		if got := displayWidth(rendered); got > width {
			t.Errorf("assistant at %d: widest line is %d columns, which wraps:\n%s", width, got, stripANSI(rendered))
		}
	}
}

// TestUserAndThinkingWideGlyphsStayInsideTheFrame covers the other two
// renderers that wrap: the user prompt and the reasoning body.
func TestUserAndThinkingWideGlyphsStayInsideTheFrame(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	for _, width := range []int{40, 60, 80} {
		user := renderUserBlock(block{kind: blockUser, text: wideText}, styles, width)
		if got := displayWidth(user); got > width {
			t.Errorf("user at %d: widest line is %d columns:\n%s", width, got, stripANSI(user))
		}
		thinking := renderThinkingBlock(block{kind: blockThinking, reasoning: wideText}, styles, width, true)
		if got := displayWidth(thinking); got > width {
			t.Errorf("thinking at %d: widest line is %d columns:\n%s", width, got, stripANSI(thinking))
		}
	}
}

// TestWideGlyphFrameNeverExceedsTheTerminal drives the real View, so the
// viewport and the frame clamp are exercised together.
func TestWideGlyphFrameNeverExceedsTheTerminal(t *testing.T) {
	for _, size := range []struct{ width, height int }{
		{120, 40}, {80, 24}, {60, 18}, {50, 16},
	} {
		model := chatModel(t)
		resize(model, size.width, size.height)
		model.blocks = append(model.blocks,
			block{kind: blockUser, text: wideText},
			block{kind: blockAssistant, text: strings.Repeat(wideText+"\n", 8)},
		)
		model.refresh()
		if got := displayWidth(model.View()); got > size.width {
			t.Errorf("at %dx%d the widest frame line is %d columns", size.width, size.height, got)
		}
	}
}
