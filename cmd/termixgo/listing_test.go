package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestFirstSentenceClipsOnARuneBoundary is the listing's multi-byte case.
//
// A tool description comes from an MCP server, which may write it in any
// language. Cutting at byte 70 lands inside a character, so the listing printed
// invalid UTF-8, which the terminal paints as a replacement glyph.
func TestFirstSentenceClipsOnARuneBoundary(t *testing.T) {
	// Ten three-byte characters put a character boundary across offset 70.
	text := strings.Repeat("\u65e5\u672c\u8a9e", 30)

	got := firstSentence(text)
	if !utf8.ValidString(got) {
		t.Fatalf("the clip produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("a clipped description should say it was clipped: %q", got)
	}
	if len(got) > 73 {
		t.Errorf("the clip ran past the cap: %d bytes", len(got))
	}
	if !strings.HasPrefix(text, strings.TrimSuffix(got, "...")) {
		t.Errorf("the clip should keep the head of the description: %q", got)
	}
}

// TestFirstSentenceCollapsesWhitespaceAndKeepsShortText is the pass-through case.
func TestFirstSentenceCollapsesWhitespaceAndKeepsShortText(t *testing.T) {
	if got := firstSentence("read   a\nfile"); got != "read a file" {
		t.Errorf("got %q, want the whitespace collapsed", got)
	}
	if got := firstSentence("short"); got != "short" {
		t.Errorf("got %q, want it unchanged", got)
	}
}
