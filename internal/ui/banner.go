package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// bannerWord is what the art spells, letter by letter.
const bannerWord = "TERMIXGO"

// bannerGlyphs is one five-row glyph per letter of bannerWord, drawn with
// full-block strokes so the wordmark reads bold on any font that can render
// U+2588. Kept as data so a test can prove every glyph has five rows and
// that the assembled art stays rectangular.
var bannerGlyphs = [][5]string{
	{"█████", "  █  ", "  █  ", "  █  ", "  █  "},        // T
	{"█████", "█    ", "████ ", "█    ", "█████"},        // E
	{"████ ", "█   █", "████ ", "█  █ ", "█   █"},        // R
	{"█   █", "██ ██", "█ █ █", "█   █", "█   █"},        // M
	{"███", " █ ", " █ ", " █ ", "███"},                  // I
	{"█   █", " █ █ ", "  █  ", " █ █ ", "█   █"},        // X
	{" █████ ", "█     ", "█ ████", "█   █ ", " █████ "}, // G
	{" ███ ", "█   █", "█   █", "█   █", " ███ "},        // O
}

// glyphWidth is the uniform row width inside one glyph.
func glyphWidth(glyph [5]string) int {
	return lipgloss.Width(glyph[0])
}

// bannerRows assembles the plain art with every row padded to the same width.
func bannerRows() []string {
	rows := make([]string, len(bannerGlyphs[0]))
	for _, glyph := range bannerGlyphs {
		for index, line := range glyph {
			rows[index] += line + " "
		}
	}
	width := 0
	for _, row := range rows {
		if length := lipgloss.Width(row); length > width {
			width = length
		}
	}
	for index, row := range rows {
		rows[index] = row + strings.Repeat(" ", width-lipgloss.Width(row))
	}
	return rows
}

// bannerHues is one colour per letter of the wordmark: a cool-to-warm ramp
// that makes the mark read as a gradient across the word rather than down
// the rows, which is what gives a bold block font its depth.
var bannerHues = []lipgloss.Color{
	lipgloss.Color("#2dd4bf"), // T teal
	lipgloss.Color("#22d3ee"), // E cyan
	lipgloss.Color("#38bdf8"), // R sky
	lipgloss.Color("#818cf8"), // M indigo
	lipgloss.Color("#a78bfa"), // I violet
	lipgloss.Color("#e879f9"), // X fuchsia
	lipgloss.Color("#fb7185"), // G rose
	lipgloss.Color("#fbbf24"), // O amber
}

// RenderBanner returns the bold coloured TERMIXGO wordmark. Each letter
// carries its own hue; the rows stay rectangular so the welcome block can
// clip them safely on narrow terminals.
func RenderBanner(styles Styles) string {
	var builder strings.Builder
	for row := range bannerGlyphs[0] {
		var line strings.Builder
		for index, glyph := range bannerGlyphs {
			hue := bannerHues[index%len(bannerHues)]
			line.WriteString(lipgloss.NewStyle().Foreground(hue).Bold(true).Render(glyph[row] + " "))
		}
		builder.WriteString(line.String())
		if row < len(bannerGlyphs[0])-1 {
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

// BannerWord exposes the wordmark text for tests.
func BannerWord() string { return bannerWord }
