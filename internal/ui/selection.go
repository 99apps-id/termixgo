package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Mouse text selection and smooth scrolling for the transcript.
//
// The transcript is a bubbles viewport holding one rendered string. A selection
// is therefore expressed in content coordinates: a line index into the rendered
// transcript and a display column inside that line. The screen point of a mouse
// event is mapped onto those coordinates using the header height above the
// viewport and the viewport's own YOffset.

// selPoint is a position in the rendered transcript.
type selPoint struct {
	line int
	col  int
}

// Smooth scroll tuning. A wheel notch moves a few lines toward a target, and a
// short tick eases the offset so the transcript slides instead of jumping.
const (
	wheelStepLines = 3
	scrollFrame    = 15 * time.Millisecond
)

// scrollTickMsg drives one step of the smooth scroll animation.
type scrollTickMsg struct{}

// scrollTick schedules the next animation frame.
func scrollTick() tea.Cmd {
	return tea.Tick(scrollFrame, func(time.Time) tea.Msg { return scrollTickMsg{} })
}

// maxScroll is the largest valid YOffset for the current content and height.
func (m *Model) maxScroll() int {
	return max(0, len(m.contentLines)-m.viewport.Height)
}

// scrollBy eases the transcript toward a new offset. The wheel sets a target
// and the returned tick moves the viewport in steps until it is reached, so a
// notch glides rather than teleports.
//
// When no animation is in flight, the actual viewport position is used as the
// base rather than the stored target. This prevents a snap-to-start when the
// first wheel event arrives after a GotoBottom or keyboard jump: those calls
// update viewport.YOffset directly but do not update scrollTarget, so using a
// stale scrollTarget of 0 would launch an animation from the current offset
// all the way back to 0.
func (m *Model) scrollBy(delta int) tea.Cmd {
	base := m.scrollTarget
	if !m.scrollTicking {
		// No animation in flight: synchronise with the real position first.
		base = m.viewport.YOffset
	}
	target := base + delta
	if target < 0 {
		target = 0
	}
	if limit := m.maxScroll(); target > limit {
		target = limit
	}
	m.scrollTarget = target
	if target == m.viewport.YOffset || m.scrollTicking {
		return nil
	}
	m.scrollTicking = true
	return scrollTick()
}

// stepScroll advances the viewport one animation step toward the target.
func (m *Model) stepScroll() {
	// The content can shrink under a running animation (a /new clears the
	// transcript), so a target that was valid when the wheel set it can now sit
	// past the end. The viewport clamps YOffset below it, the equality check in
	// the tick loop never fires, and the animation would spin forever. Clamp
	// the target first so the loop can finish.
	if limit := m.maxScroll(); m.scrollTarget > limit {
		m.scrollTarget = limit
	}
	if m.scrollTarget < 0 {
		m.scrollTarget = 0
	}
	current := m.viewport.YOffset
	diff := m.scrollTarget - current
	if diff == 0 {
		return
	}
	step := diff / 3
	if step == 0 {
		if diff > 0 {
			step = 1
		} else {
			step = -1
		}
	}
	m.viewport.SetYOffset(current + step)
}

// transcriptTop is the screen row where the viewport starts, which is the
// header's height.
func (m *Model) transcriptTop() int {
	return lipgloss.Height(m.viewHeader())
}

// mousePoint maps a mouse event to a transcript coordinate. inside reports
// whether the event is over the transcript band, so a press elsewhere does not
// start a selection while a drag past an edge still extends one.
func (m *Model) mousePoint(msg tea.MouseMsg) (point selPoint, inside bool) {
	top := m.transcriptTop()
	inside = msg.Y >= top && msg.Y < top+m.viewport.Height
	line := m.viewport.YOffset + (msg.Y - top)
	if line < 0 {
		line = 0
	}
	if last := len(m.contentLines) - 1; last >= 0 && line > last {
		line = last
	}
	return selPoint{line: line, col: max(0, msg.X)}, inside
}

// clearSelection drops any active selection and repaints without the highlight.
func (m *Model) clearSelection() {
	if !m.selecting && !m.selActive {
		return
	}
	m.selecting = false
	m.selActive = false
	m.refresh()
}

// handleMouse routes a mouse event to the selection or the smooth scroll.
func (m *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		return m, m.scrollBy(-wheelStepLines)
	case tea.MouseButtonWheelDown:
		return m, m.scrollBy(wheelStepLines)
	}

	switch msg.Action {
	case tea.MouseActionPress:
		if msg.Button != tea.MouseButtonLeft {
			return m, nil
		}
		// A press in the composer starts a partial selection there; the
		// transcript selection and the Ctrl+A state are dropped so only one
		// selection is live.
		if m.hintsVisible() && m.inComposer(msg) {
			m.composerSelAnchor = m.composerPoint(msg)
			m.composerSelFocus = m.composerSelAnchor
			m.composerSelActive = true
			m.composerSelected = false
			m.selecting = false
			m.selActive = false
			m.refresh()
			return m, nil
		}
		point, inside := m.mousePoint(msg)
		if !inside {
			// A click outside the transcript takes the highlight away.
			m.selecting = false
			m.selActive = false
			m.refresh()
			return m, nil
		}
		m.selAnchor = point
		m.selFocus = point
		m.selecting = true
		m.selActive = false
		m.refresh()
		return m, nil
	case tea.MouseActionMotion:
		if m.composerSelActive {
			m.composerSelFocus = m.composerPoint(msg)
			m.refresh()
			return m, nil
		}
		if !m.selecting {
			return m, nil
		}
		point, _ := m.mousePoint(msg)
		m.selFocus = point
		m.refresh()
		return m, nil
	case tea.MouseActionRelease:
		if m.composerSelActive {
			m.composerSelFocus = m.composerPoint(msg)
			return m.copyComposerRange()
		}
		if !m.selecting {
			return m, nil
		}
		m.selecting = false
		m.selFocus, _ = m.mousePoint(msg)
		text := selectionText(m.contentLines, m.selAnchor, m.selFocus)
		if strings.TrimSpace(text) == "" {
			m.selActive = false
			m.refresh()
			return m, nil
		}
		m.selActive = true
		m.notice = fmt.Sprintf("Copied %d characters.", utf8.RuneCountInString(text))
		m.refresh()
		return m, writeClipboard(text)
	}
	return m, nil
}

// copyComposerRange copies the composer's drag selection and clears the
// highlight, so typing afterwards is not confused by a stale selection.
func (m *Model) copyComposerRange() (tea.Model, tea.Cmd) {
	text := m.composerSelectionText()
	m.composerSelActive = false
	if strings.TrimSpace(text) == "" {
		m.refresh()
		return m, nil
	}
	m.notice = fmt.Sprintf("Copied %d characters.", utf8.RuneCountInString(text))
	m.refresh()
	return m, writeClipboard(text)
}

// copyComposerSelection copies the whole composer and clears the selection.
func (m *Model) copyComposerSelection() (tea.Model, tea.Cmd) {
	text := m.composer.Value()
	m.composerSelected = false
	if strings.TrimSpace(text) == "" {
		m.refresh()
		return m, nil
	}
	m.notice = fmt.Sprintf("Copied %d characters.", utf8.RuneCountInString(text))
	m.refresh()
	return m, writeClipboard(text)
}

// copyTranscriptSelection copies the mouse selection and drops the highlight.
func (m *Model) copyTranscriptSelection() (tea.Model, tea.Cmd) {
	text := selectionText(m.contentLines, m.selAnchor, m.selFocus)
	m.selActive = false
	if strings.TrimSpace(text) == "" {
		m.refresh()
		return m, nil
	}
	m.notice = fmt.Sprintf("Copied %d characters.", utf8.RuneCountInString(text))
	m.refresh()
	return m, writeClipboard(text)
}

// deletesComposerSelection reports a key that empties a select-all rather than
// overwriting it.
func deletesComposerSelection(key tea.KeyMsg) bool {
	switch key.Type {
	case tea.KeyBackspace, tea.KeyDelete:
		return true
	}
	return false
}

// replacesComposerSelection reports a key that consumes a select-all: a
// printable rune, a space or a paste overwrites it, backspace and delete clear
// it.
func replacesComposerSelection(key tea.KeyMsg) bool {
	return key.Type == tea.KeyRunes || key.Type == tea.KeySpace || key.Paste || deletesComposerSelection(key)
}

// orderedSelection returns a selection with its start before its end, so a drag
// in any direction reads the same.
func orderedSelection(a, b selPoint) (selPoint, selPoint) {
	if a.line < b.line || (a.line == b.line && a.col <= b.col) {
		return a, b
	}
	return b, a
}

// selectionText is the plain text inside a selection over content lines. The
// styling is stripped and each line is cut to its selected columns.
func selectionText(lines []string, a, b selPoint) string {
	start, end := orderedSelection(a, b)
	if len(lines) == 0 {
		return ""
	}
	if start.line < 0 {
		start.line = 0
	}
	if end.line >= len(lines) {
		end.line = len(lines) - 1
	}
	if start.line > end.line {
		return ""
	}
	var out []string
	for line := start.line; line <= end.line; line++ {
		from, to := 0, ansi.StringWidth(lines[line])
		if line == start.line {
			from = start.col
		}
		if line == end.line {
			to = end.col
		}
		if to < from {
			to = from
		}
		out = append(out, strings.TrimRight(ansi.Strip(ansi.Cut(lines[line], from, to)), " "))
	}
	return strings.Join(out, "\n")
}

// highlightSelection returns the content lines with the selection drawn in
// reverse video.
func highlightSelection(lines []string, a, b selPoint) []string {
	start, end := orderedSelection(a, b)
	out := make([]string, len(lines))
	copy(out, lines)
	if len(lines) == 0 {
		return out
	}
	for line := max(0, start.line); line <= end.line && line < len(lines); line++ {
		from, to := 0, ansi.StringWidth(lines[line])
		if line == start.line {
			from = start.col
		}
		if line == end.line {
			to = end.col
		}
		if to <= from {
			continue
		}
		out[line] = insertHighlight(lines[line], from, to)
	}
	return out
}

// insertHighlight wraps display columns [start,end) of a styled line in reverse
// video. The original bytes are copied verbatim, so every escape sequence keeps
// its colour and only the two boundaries gain a reverse toggle.
func insertHighlight(line string, start, end int) string {
	if end <= start || start < 0 {
		return line
	}
	var b strings.Builder
	b.Grow(len(line) + 8)
	col := 0
	opened := false
	for i := 0; i < len(line); {
		if !opened && col >= start {
			b.WriteString("\x1b[7m")
			opened = true
		}
		if opened && col >= end {
			b.WriteString("\x1b[27m")
			opened = false
		}
		if line[i] == 0x1b {
			j := skipEscape(line, i)
			b.WriteString(line[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		b.WriteRune(r)
		col += ansi.StringWidth(string(r))
		i += size
	}
	if opened {
		b.WriteString("\x1b[27m")
	}
	return b.String()
}

// skipEscape returns the index just past the escape sequence at i: a CSI runs
// to its final byte, an OSC to BEL or ST, anything else is two bytes.
func skipEscape(s string, i int) int {
	j := i + 1
	if j >= len(s) {
		return j
	}
	switch s[j] {
	case '[':
		j++
		for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
			j++
		}
		if j < len(s) {
			j++
		}
	case ']':
		j++
		for j < len(s) {
			if s[j] == 0x07 {
				j++
				break
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				j += 2
				break
			}
			j++
		}
	default:
		j++
	}
	return j
}
