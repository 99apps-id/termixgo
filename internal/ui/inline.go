package ui

import (
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/99apps-id/termixgo/internal/config"
)

// Inline layout
//
// In the inline layout the program does not take over the screen. A block is
// printed once into the terminal's scrollback as soon as it is final, and the
// redrawn area is only what is still changing: the live tail of the turn in
// flight, the status line and the composer. That is how Claude Code works,
// and it hands scrolling, selection and copy back to the terminal.

// inlineLayout reports whether the configuration asks for the inline layout.
// It is the default; "fullscreen" or TERMIXGO_FULLSCREEN=1 selects the
// alternate screen.
func inlineLayout(cfg config.Config) bool {
	if value := strings.TrimSpace(os.Getenv("TERMIXGO_FULLSCREEN")); value != "" && value != "0" {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(cfg.TUILayout), "fullscreen")
}

// settledCount is how many leading blocks are final. With no turn running
// every block is; during a turn the last block is still growing, a running
// tool or an open thinking block still changes, and order is kept by stopping
// at the first block that is not final.
func (m *Model) settledCount() int {
	total := len(m.blocks)
	if !m.running {
		return total
	}
	for index := m.printed; index < total; index++ {
		item := m.blocks[index]
		if index == total-1 || item.running || (item.kind == blockThinking && !item.finalized) {
			return index
		}
	}
	return total
}

// flushSettled queues every newly final block for printing into the
// scrollback. A transcript that was reset (/new, /clear) starts over.
func (m *Model) flushSettled() {
	// Nothing is printed while the transcript pager holds the alternate
	// screen: a print there is dropped, so it waits until the pager closes.
	if !m.inline || m.current == modeTranscript {
		return
	}
	if m.printed > len(m.blocks) {
		m.printed = 0
	}
	end := m.settledCount()
	if end <= m.printed {
		return
	}
	hide := m.app != nil && !m.app.Config().ShowReasoning
	for ; m.printed < end; m.printed++ {
		rendered := transcript(hideReasoning(m.blocks[m.printed:m.printed+1], hide), m.styles, m.width, m.showDetails)
		if strings.TrimSpace(rendered) != "" {
			m.pendingPrint = append(m.pendingPrint, rendered)
		}
	}
}

// takePrints wraps cmd so the queued blocks are printed above the live area
// before anything else the update asked for.
func (m *Model) takePrints(cmd tea.Cmd) tea.Cmd {
	if !m.inline || len(m.pendingPrint) == 0 {
		return cmd
	}
	text := strings.Join(m.pendingPrint, "\n\n") + "\n"
	m.pendingPrint = nil
	if cmd == nil {
		return tea.Println(text)
	}
	return tea.Sequence(tea.Println(text), cmd)
}

// inlineView renders the live area: the blocks not yet printed, the status
// line and the composer or the open overlay. The live blocks keep their
// newest lines when they would not fit.
func (m *Model) inlineView() string {
	overlay := ""
	switch {
	case m.pendingApproval != nil:
		overlay = m.viewApproval()
	case m.pendingAsk != nil:
		overlay = m.viewAsk()
	case m.slashMenuOpen():
		overlay = m.viewSlashMenu()
	case len(m.mentionMatches) > 0:
		overlay = m.viewMentionMenu()
	}
	bottom := []string{m.viewStatus()}
	if overlay != "" {
		bottom = append(bottom, overlay)
	} else {
		bottom = append(bottom, m.viewComposer(), m.viewHints())
	}
	bottomText := lipgloss.JoinVertical(lipgloss.Left, bottom...)

	var sections []string
	if m.printed < len(m.blocks) {
		hide := m.app != nil && !m.app.Config().ShowReasoning
		live := transcript(hideReasoning(m.blocks[m.printed:], hide), m.styles, m.width, m.showDetails)
		if strings.TrimSpace(live) != "" {
			lines := strings.Split(live, "\n")
			budget := m.height - lipgloss.Height(bottomText) - 1
			if budget < 1 {
				budget = 1
			}
			if len(lines) > budget {
				lines = lines[len(lines)-budget:]
			}
			sections = append(sections, strings.Join(lines, "\n"), "")
		}
	}
	sections = append(sections, bottomText)
	return strings.Join(sections, "\n")
}
