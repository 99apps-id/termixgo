package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// widestDisplayLine is the widest rendered line measured in terminal columns.
func widestDisplayLine(text string) int {
	widest := 0
	for _, line := range strings.Split(stripANSI(text), "\n") {
		if width := lipgloss.Width(line); width > widest {
			widest = width
		}
	}
	return widest
}

// TestNoBlockWrapsALongUnbreakableWord is the frame-shift guard. A URL, a hash
// or a long identifier has no space to wrap at; leaving it whole produced a line
// wider than the terminal, the terminal wrapped it on its own and every row
// below shifted, which read as scrambled text. Every block kind must fit.
func TestNoBlockWrapsALongUnbreakableWord(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	long := strings.Repeat("x", 300)
	wide := strings.Repeat("日", 120)
	url := "https://example.com/" + strings.Repeat("a", 200)

	blocks := map[string]block{
		"assistant":      {kind: blockAssistant, text: "prefix " + long + " suffix"},
		"reasoning":      {kind: blockThinking, reasoning: "prefix " + long + " suffix"},
		"user":           {kind: blockUser, text: "prefix " + long + " suffix"},
		"notice":         {kind: blockNotice, text: "prefix " + long + " suffix"},
		"error":          {kind: blockError, text: "prefix " + long + " suffix"},
		"code fence":     {kind: blockAssistant, text: "```\n" + long + "\n```"},
		"bold span":      {kind: blockAssistant, text: "**" + long + "**"},
		"code span":      {kind: blockAssistant, text: "`" + long + "`"},
		"bold spaces":    {kind: blockAssistant, text: "**" + strings.Repeat("word ", 40) + "**"},
		"url":            {kind: blockAssistant, text: url},
		"wide glyphs":    {kind: blockAssistant, text: wide},
		"wide reasoning": {kind: blockThinking, reasoning: wide},
	}
	for name, item := range blocks {
		for _, width := range []int{40, 60, 80} {
			rendered := transcript([]block{item}, styles, width, true)
			if got := widestDisplayLine(rendered); got > width {
				t.Errorf("%s at %d columns: widest line is %d:\n%s", name, width, got, stripANSI(rendered))
			}
		}
	}
}

// TestHardSplitPreservesTheWord checks that splitting a word never drops,
// duplicates or reorders a character; the pieces join back to the original.
func TestHardSplitPreservesTheWord(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("a", 25),
		"abcdefghijklmnopqrstuvwxyz0123456789",
		strings.Repeat("日", 10),
	} {
		// Widths below 2 cannot hold a wide glyph, which no caller uses: the
		// transcript clamps to at least 24 columns.
		for _, width := range []int{2, 5, 8} {
			pieces := hardSplit(text, width)
			if len(pieces) < 2 {
				t.Fatalf("hardSplit(%q, %d) did not split", text, width)
			}
			if joined := strings.Join(pieces, ""); joined != text {
				t.Errorf("hardSplit(%q, %d) joined = %q", text, width, joined)
			}
			for _, piece := range pieces {
				if got := lipgloss.Width(piece); got > width {
					t.Errorf("piece %q is %d columns, over %d", piece, got, width)
				}
			}
		}
	}
}

// TestFullFrameFitsWithALongWord drives the whole View, so the viewport and the
// frame clamp are exercised with a word that cannot wrap.
func TestFullFrameFitsWithALongWord(t *testing.T) {
	for _, size := range []struct{ width, height int }{
		{120, 40}, {80, 24}, {60, 18}, {50, 16},
	} {
		model := chatModel(t)
		resize(model, size.width, size.height)
		long := strings.Repeat("z", 400)
		model.blocks = append(model.blocks,
			block{kind: blockUser, text: "look at " + long},
			block{kind: blockAssistant, text: "see " + long + " and `" + long + "`"},
		)
		model.refresh()
		if got := widestDisplayLine(model.View()); got > size.width {
			t.Errorf("at %dx%d the widest frame line is %d columns", size.width, size.height, got)
		}
	}
}
