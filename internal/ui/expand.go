package ui

import (
	"strings"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// clickToExpand is set while the program captures the mouse (the fullscreen
// layout). The inline layout leaves the mouse to the terminal so the scrollback
// keeps working, and there the keyboard toggle is the only way to expand.
var clickToExpand atomic.Bool

// expandHint is the cue appended to a clipped line.
func expandHint() string {
	if clickToExpand.Load() {
		return " (click or ctrl+o to expand)"
	}
	return " (ctrl+o to expand)"
}

// blockSpan is the rows of the rendered transcript one part occupies and the
// blocks it was drawn from, so a click on a row can be traced to its blocks.
// first and last are indexes into the slice that was rendered; last is
// exclusive.
type blockSpan struct {
	startRow, endRow int
	first, last      int
}

// spanAt finds the span holding a row of the rendered transcript.
func spanAt(spans []blockSpan, row int) (blockSpan, bool) {
	for _, span := range spans {
		if row >= span.startRow && row < span.endRow {
			return span, true
		}
	}
	return blockSpan{}, false
}

// storedIndex maps an index of the rendered block slice back to m.blocks. The
// rendered slice can start with a synthetic "earlier blocks are folded" notice,
// which maps to nothing.
func (m *Model) storedIndex(rendered int) int {
	if len(m.blocks) > transcriptWindowBlocks {
		if rendered == 0 {
			return -1
		}
		return len(m.blocks) - transcriptWindowBlocks + rendered - 1
	}
	return rendered
}

// toggleExpandedAt opens or closes the tool or thinking blocks drawn at a
// transcript row. It reports whether the row held something to toggle.
func (m *Model) toggleExpandedAt(row int) bool {
	span, ok := spanAt(m.spans, row)
	if !ok {
		return false
	}
	first := -1
	for index := span.first; index < span.last; index++ {
		stored := m.storedIndex(index)
		if stored < 0 || stored >= len(m.blocks) {
			continue
		}
		if kind := m.blocks[stored].kind; kind == blockTool || kind == blockThinking {
			first = stored
			break
		}
	}
	if first < 0 {
		return false
	}
	open := !m.blocks[first].expanded
	for index := span.first; index < span.last; index++ {
		stored := m.storedIndex(index)
		if stored < 0 || stored >= len(m.blocks) {
			continue
		}
		if kind := m.blocks[stored].kind; kind == blockTool || kind == blockThinking {
			m.blocks[stored].expanded = open
		}
	}
	return true
}

// clickExpand handles a plain click (a press and release on one cell) on the
// transcript: it opens or closes the block under the pointer.
func (m *Model) clickExpand(point selPoint) (tea.Model, tea.Cmd, bool) {
	if !m.toggleExpandedAt(point.line) {
		return m, nil, false
	}
	m.selActive = false
	m.refresh()
	return m, nil, true
}

// wrapHanging wraps text to width with every line after the first indented, the
// way a long command or path reads under its marker. A text that needs more than
// limit lines is cut there with an ellipsis, so a clipped label never reads as
// a whole one.
func wrapHanging(text string, width, hang, limit int) []string {
	if width < hang+8 {
		width = hang + 8
	}
	lines := strings.Split(wrapPlain(strings.TrimSpace(text), width-hang), "\n")
	if limit > 0 && len(lines) > limit {
		lines = append(lines[:limit-1], truncate(lines[limit-1]+" ...", width-hang))
	}
	for index := 1; index < len(lines); index++ {
		lines[index] = strings.Repeat(" ", hang) + lines[index]
	}
	return lines
}

// widthOf is the display width of text.
func widthOf(text string) int { return ansi.StringWidth(text) }
