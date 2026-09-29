package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/99apps-id/termixgo/internal/agent"
)

func TestBannerIsRectangular(t *testing.T) {
	rows := bannerRows()
	if len(rows) != 5 {
		t.Fatalf("expected five rows, got %d", len(rows))
	}
	width := lipgloss.Width(rows[0])
	if width == 0 {
		t.Fatalf("the banner has no width")
	}
	for index, row := range rows {
		if got := lipgloss.Width(row); got != width {
			t.Errorf("row %d width = %d, want %d", index, got, width)
		}
	}
}

func TestBannerHasOneGlyphPerLetter(t *testing.T) {
	if len(bannerGlyphs) != len(BannerWord()) {
		t.Fatalf("expected %d glyphs, got %d", len(BannerWord()), len(bannerGlyphs))
	}
	for index, glyph := range bannerGlyphs {
		for row, line := range glyph {
			if strings.TrimSpace(line) == "" {
				t.Errorf("glyph %d (%c) row %d is empty", index, BannerWord()[index], row)
			}
		}
	}
}

func TestRenderBannerContainsArt(t *testing.T) {
	rendered := RenderBanner(NewStyles(DefaultPalette()))
	if !strings.Contains(stripANSI(rendered), "|_   _|") {
		t.Errorf("the rendered banner is missing the T glyph:\n%s", rendered)
	}
}

func TestParseSlash(t *testing.T) {
	cases := []struct {
		input string
		name  string
		args  string
		ok    bool
	}{
		{"/model", "model", "", true},
		{"/model claude-sonnet-4-5", "model", "claude-sonnet-4-5", true},
		{"  /TRUST on  ", "trust", "on", true},
		{"hello", "", "", false},
		{"/setup extra words", "setup", "extra words", true},
	}
	for _, testCase := range cases {
		name, args, ok := ParseSlash(testCase.input)
		if ok != testCase.ok || name != testCase.name || args != testCase.args {
			t.Errorf("ParseSlash(%q) = (%q, %q, %v), want (%q, %q, %v)",
				testCase.input, name, args, ok, testCase.name, testCase.args, testCase.ok)
		}
	}
}

func TestMatchSlashOnlyBeforeArguments(t *testing.T) {
	if matches := MatchSlash("/model x"); len(matches) != 0 {
		t.Errorf("a command with arguments should close the menu, got %d", len(matches))
	}
	matches := MatchSlash("/mo")
	if len(matches) != 1 || matches[0].Trigger != "/model" {
		t.Errorf("expected /model, got %+v", matches)
	}
	if matches := MatchSlash("model"); len(matches) != 0 {
		t.Errorf("a command needs its leading slash")
	}
}

func TestSlashHelpIsTabSeparated(t *testing.T) {
	for _, entry := range SlashHelp() {
		if !strings.Contains(entry, "\t") {
			t.Errorf("help entry %q should separate usage and summary with a tab", entry)
		}
	}
}

func TestWrapPlainRespectsWidth(t *testing.T) {
	text := "the quick brown fox jumps over the lazy dog and keeps running"
	wrapped := wrapPlain(text, 20)
	for _, line := range strings.Split(wrapped, "\n") {
		if len([]rune(line)) > 20 {
			t.Errorf("line %q exceeds the width", line)
		}
	}
	if !strings.Contains(wrapped, "\n") {
		t.Errorf("a long line should wrap")
	}
}

func TestWrapPlainHardSplitsLongWords(t *testing.T) {
	// A word longer than the width is hard-split: every line fits, and the
	// pieces join back to the original word with no character lost, added or
	// reordered. Leaving it whole made the terminal wrap the line on its own and
	// shift the frame, which is what looked scrambled.
	const word = "shortandveryveryverylongword"
	wrapped := wrapPlain(word, 10)
	for _, line := range strings.Split(wrapped, "\n") {
		if len([]rune(line)) > 10 {
			t.Errorf("line %q exceeds the width", line)
		}
	}
	if joined := strings.ReplaceAll(wrapped, "\n", ""); joined != word {
		t.Errorf("split word joined = %q, want %q", joined, word)
	}
	if !strings.Contains(wrapped, "\n") {
		t.Errorf("a word longer than the width should be split")
	}
}

func TestRenderMarkdownStylesHeadingsAndCode(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	rendered := renderMarkdown("# Title\n\n- one\n- two\n\n```\ncode line\n```\n", styles, 40)
	plain := stripANSI(rendered)
	if !strings.Contains(plain, "Title") {
		t.Errorf("heading text lost: %q", plain)
	}
	if !strings.Contains(plain, "one") || !strings.Contains(plain, "two") {
		t.Errorf("bullets lost: %q", plain)
	}
	if !strings.Contains(plain, "code line") {
		t.Errorf("code block lost: %q", plain)
	}
	if strings.Contains(plain, "```") {
		t.Errorf("fences should not be rendered: %q", plain)
	}
}

func TestTruncateKeepsItShort(t *testing.T) {
	if got := truncate("abcdefghij", 5); lipgloss.Width(got) > 5 {
		t.Errorf("truncate produced %q which is too wide", got)
	}
	if got := truncate("short", 20); got != "short" {
		t.Errorf("short text should pass through, got %q", got)
	}
}

func TestTranscriptRendersEveryBlockKind(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	blocks := []block{
		{kind: blockWelcome, text: "welcome"},
		{kind: blockUser, text: "do the thing"},
		{kind: blockAssistant, text: "Done."},
		{kind: blockThinking, running: true, reasoning: "considering"},
		{kind: blockTool, toolLabel: "Read main.go", toolOK: true, toolMillis: 12},
		{kind: blockPlan, plan: []agent.Todo{{Title: "Ship it", Status: "in_progress"}}},
		{kind: blockNotice, text: "note"},
		{kind: blockError, text: "boom"},
	}
	rendered := stripANSI(transcript(blocks, styles, 60, true))
	for _, want := range []string{"welcome", "do the thing", "Done.", "considering", "Read main.go", "boom", "note"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("transcript is missing %q:\n%s", want, rendered)
		}
	}
}

func TestApprovalDecisionWords(t *testing.T) {
	if decisionWord(0) != "denied" {
		t.Errorf("DecisionDeny should read as denied")
	}
	if !strings.Contains(decisionWord(3), "always") {
		t.Errorf("DecisionAllowAlways should read as always allowed")
	}
}
