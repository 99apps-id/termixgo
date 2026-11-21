package ui

import (
	"strings"
	"testing"
)

func md(t *testing.T, text string, width int) string {
	t.Helper()
	rendered := stripANSI(renderMarkdown(text, NewStyles(DefaultPalette()), width))
	for _, line := range strings.Split(rendered, "\n") {
		if w := len([]rune(line)); w > width {
			t.Fatalf("line wider than %d (%d): %q", width, w, line)
		}
	}
	return rendered
}

func TestMarkdownTableAlignsColumns(t *testing.T) {
	out := md(t, "| Name | Size |\n|---|---:|\n| main.go | 12 KB |\n| a | 1 |", 60)
	lines := strings.Split(out, "\n")
	if len(lines) != 4 {
		t.Fatalf("header, rule and two rows expected:\n%s", out)
	}
	if !strings.Contains(lines[1], "─┼─") {
		t.Fatalf("the header rule should be drawn:\n%s", out)
	}
	if strings.Index(lines[2], "│") != strings.Index(lines[3], "│") || strings.Index(lines[0], "│") != strings.Index(lines[2], "│") {
		t.Fatalf("columns should line up:\n%s", out)
	}
}

func TestMarkdownTableShrinksToTheWidth(t *testing.T) {
	out := md(t, "| a | b |\n|---|---|\n| "+strings.Repeat("x", 80)+" | "+strings.Repeat("y", 80)+" |", 40)
	if !strings.Contains(out, "...") {
		t.Fatalf("over-wide cells should be clipped:\n%s", out)
	}
}

func TestMarkdownBlocks(t *testing.T) {
	out := md(t, strings.Join([]string{
		"# Title",
		"#hashtag stays prose",
		"> quoted advice",
		"---",
		"- top",
		"  - nested",
		"see [docs](https://example.com/docs) now",
		"~~~go",
		"x := 1 // " + strings.Repeat("long", 30),
		"~~~",
	}, "\n"), 60)
	for _, want := range []string{"Title", "#hashtag stays prose", "│ quoted advice", "────", "• top", "  • nested", "docs (https://example.com/docs)", "┌─ go", "└"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[docs]") {
		t.Fatalf("link markup should be rendered:\n%s", out)
	}
	if !strings.Contains(out, strings.Repeat("long", 5)) || strings.Contains(out, "...") {
		t.Fatalf("code should wrap, not clip:\n%s", out)
	}
}

func TestMarkdownFenceKindsDoNotCloseEachOther(t *testing.T) {
	out := md(t, "~~~\n```\nstill code\n~~~\nprose", 60)
	if !strings.Contains(out, "  ```") || !strings.Contains(out, "prose") {
		t.Fatalf("a ``` inside a ~~~ fence is code:\n%s", out)
	}
}
