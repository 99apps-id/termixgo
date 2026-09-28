package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// bannerWord is what the art spells, letter by letter.
const bannerWord = "TERMIXGO"

// bannerGlyphs is one five-row glyph per letter of bannerWord. Kept as data so
// a test can prove every glyph has five rows and that the assembled art stays
// rectangular.
var bannerGlyphs = [][5]string{
	{" _____ ", "|_   _|", "  | |  ", "  | |  ", "  |_|  "},        // T
	{" _____ ", "| ____|", "|  _|  ", "| |___ ", "|_____|"},        // E
	{" ____  ", "|  _ \\ ", "| |_) |", "|  _ < ", "|_| \\_\\"},     // R
	{" __  __ ", "|  \\/  |", "| |\\/| |", "| |  | |", "|_|  |_|"}, // M
	{" ___ ", "|_ _|", " | | ", " | | ", "|___|"},                  // I
	{"__  __", "\\ \\/ /", " \\  / ", " /  \\ ", "/_/\\_\\"},       // X
	{"  ____ ", " / ___|", "| |  _ ", "| |_| |", " \\____|"},       // G
	{"  ___  ", " / _ \\ ", "| | | |", "| |_| |", " \\___/ "},      // O
}

// bannerRows assembles the art with every row padded to the same width, then
// trims the trailing columns that carry no shape.
func bannerRows() []string {
	rows := make([]string, len(bannerGlyphs[0]))
	for _, glyph := range bannerGlyphs {
		for index, line := range glyph {
			rows[index] += line
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

// bannerGradient is the per-row colour ramp of the wordmark.
var bannerGradient = []lipgloss.Color{
	lipgloss.Color("#5eead4"),
	lipgloss.Color("#67e8f9"),
	lipgloss.Color("#7dd3fc"),
	lipgloss.Color("#93c5fd"),
	lipgloss.Color("#a78bfa"),
}

// RenderBanner returns the coloured TERMIXGO wordmark.
func RenderBanner(styles Styles) string {
	rows := bannerRows()
	var builder strings.Builder
	for index, row := range rows {
		color := bannerGradient[index%len(bannerGradient)]
		builder.WriteString(lipgloss.NewStyle().Foreground(color).Bold(true).Render(row))
		if index < len(rows)-1 {
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

// BannerWord exposes the wordmark text for tests.
func BannerWord() string { return bannerWord }
