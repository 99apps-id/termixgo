package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Mouse text selection inside the composer.
//
// The composer is a bubbles textarea: its view is a bordered, padded box with a
// two-column prompt, and the value soft-wraps to the inner width. A selection is
// therefore expressed in the composer's rendered coordinates: a line index into
// the box's text rows and a display column inside the line, past the border,
// the padding and the prompt. Reading the rendered view (rather than the value)
// is what makes a soft-wrapped line and a scrolled draft map correctly without
// reproducing the textarea's wrap.

// composerContentLeft is the display column where composer text starts: the
// border (1), the left padding (1) and the prompt "> " (2).
const composerContentLeft = 4

// composerTopRow is the screen row of the composer's first text line. The
// composer and the hints form the last rows of the frame, and the composer view
// carries a top border line, so its text begins one row below its own top.
func (m *Model) composerTopRow() int {
	height := lipgloss.Height(m.viewComposer()) + lipgloss.Height(m.viewHints())
	return m.height - height + 1
}

// composerContentRows is how many text rows the composer shows, past its two
// border lines.
func (m *Model) composerContentRows() int {
	if rows := lipgloss.Height(m.viewComposer()) - 2; rows > 0 {
		return rows
	}
	return 0
}

// inComposer reports whether a mouse row lands on the composer's text band.
func (m *Model) inComposer(msg tea.MouseMsg) bool {
	rows := m.composerContentRows()
	if rows == 0 {
		return false
	}
	top := m.composerTopRow()
	return msg.Y >= top && msg.Y < top+rows
}

// composerPoint maps a mouse event to a composer content coordinate, where line
// 0 is the first text row and column 0 is the first column past the prompt.
func (m *Model) composerPoint(msg tea.MouseMsg) selPoint {
	line := msg.Y - m.composerTopRow()
	if line < 0 {
		line = 0
	}
	if rows := m.composerContentRows(); rows > 0 && line >= rows {
		line = rows - 1
	}
	col := msg.X - composerContentLeft
	if col < 0 {
		col = 0
	}
	return selPoint{line: line, col: col}
}

// composerSelectionText is the plain text of a composer selection, read from the
// rendered composer so a soft-wrapped line and a scrolled draft map correctly.
func (m *Model) composerSelectionText() string {
	start, end := orderedSelection(m.composerSelAnchor, m.composerSelFocus)
	lines := strings.Split(m.viewComposer(), "\n")
	var out []string
	for line := start.line; line <= end.line; line++ {
		// Skip the composer's top border row.
		index := line + 1
		if index < 0 || index >= len(lines) {
			continue
		}
		from, to := composerContentLeft, ansi.StringWidth(lines[index])
		if line == start.line {
			from = composerContentLeft + start.col
		}
		if line == end.line {
			to = composerContentLeft + end.col
		}
		if to < from {
			to = from
		}
		out = append(out, strings.TrimRight(ansi.Strip(ansi.Cut(lines[index], from, to)), " "))
	}
	return strings.Join(out, "\n")
}

// highlightComposerSelection draws the selection in reverse video over the
// rendered composer, leaving the border and the prompt unstyled.
func highlightComposerSelection(view string, a, b selPoint) string {
	start, end := orderedSelection(a, b)
	lines := strings.Split(view, "\n")
	for line := start.line; line <= end.line; line++ {
		index := line + 1 // skip the top border row
		if index < 0 || index >= len(lines) {
			continue
		}
		from, to := composerContentLeft, ansi.StringWidth(lines[index])
		if line == start.line {
			from = composerContentLeft + start.col
		}
		if line == end.line {
			to = composerContentLeft + end.col
		}
		if to <= from {
			continue
		}
		lines[index] = insertHighlight(lines[index], from, to)
	}
	return strings.Join(lines, "\n")
}
