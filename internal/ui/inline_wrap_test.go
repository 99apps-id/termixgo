package ui

import (
	"strings"
	"testing"
)

// TestInlineSpanSurvivesWrapping covers the defect that put literal backticks
// on screen: a span whose text straddles a line break. The wrapper used to
// break at any space, so a line could end inside `cargo nextest` and the
// markers were left unpaired and printed as text.
func TestInlineSpanSurvivesWrapping(t *testing.T) {
	forceColor(t)
	styles := NewStyles(DefaultPalette())
	source := "Checks resmi: `pnpm lint`, `pnpm check:types`, `pnpm test`, lalu `cargo clippy` dan `cargo nextest` di `src-tauri`."

	for _, width := range []int{40, 60, 80, 100, 118} {
		rendered := renderMarkdown(source, styles, width)
		visible := stripANSI(rendered)
		if strings.Contains(visible, "`") {
			t.Errorf("width %d left a backtick on screen:\n%s", width, visible)
		}
		// Every word of the payload must still be present, in order.
		want := strings.Fields(strings.ReplaceAll(source, "`", ""))
		got := strings.Fields(visible)
		if len(got) != len(want) {
			t.Fatalf("width %d: %d words, want %d:\n%s", width, len(got), len(want), visible)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("width %d: word %d = %q, want %q:\n%s", width, index, got[index], want[index], visible)
			}
		}
	}

	// The same line as a bullet, the shape the defect was reported in: the
	// list marker is rendered as "* " and must not disturb the span pairing.
	bullet := "- " + source
	for _, width := range []int{40, 60, 80, 118} {
		visible := stripANSI(renderMarkdown(bullet, styles, width))
		if strings.Contains(visible, "`") {
			t.Errorf("bullet at width %d left a backtick on screen:\n%s", width, visible)
		}
		for _, line := range strings.Split(visible, "\n") {
			if len([]rune(line)) > width {
				t.Errorf("bullet at width %d produced a %d-column line: %q", width, len([]rune(line)), line)
			}
		}
	}
}

// TestInlineBoldSpanSurvivesWrapping is the same contract for a bold span that
// holds inline code, the shape `**Stack (dari `package.json`):**`.
func TestInlineBoldSpanSurvivesWrapping(t *testing.T) {
	forceColor(t)
	styles := NewStyles(DefaultPalette())
	source := "**Stack (dari `package.json`):** React 19 dan TypeScript, dengan alat build yang panjang sekali supaya pembungkusnya benar benar bekerja."

	for _, width := range []int{40, 60, 80, 120} {
		visible := stripANSI(renderMarkdown(source, styles, width))
		if strings.Contains(visible, "`") || strings.Contains(visible, "**") {
			t.Errorf("width %d left a marker on screen:\n%s", width, visible)
		}
		for _, line := range strings.Split(visible, "\n") {
			if len([]rune(line)) > width {
				t.Errorf("width %d produced a %d-column line: %q", width, len([]rune(line)), line)
			}
		}
	}
}

// TestUnpairedMarkersStayLiteral pins that a lone marker is still shown as
// text rather than swallowing the rest of the line.
func TestUnpairedMarkersStayLiteral(t *testing.T) {
	forceColor(t)
	styles := NewStyles(DefaultPalette())
	got := stripANSI(renderMarkdown("harga naik 3** dan `tak tertutup", styles, 80))
	if !strings.Contains(got, "3**") {
		t.Errorf("an unpaired ** should stay visible, got %q", got)
	}
	if !strings.Contains(got, "`tak tertutup") {
		t.Errorf("an unpaired backtick should stay visible, got %q", got)
	}
}
