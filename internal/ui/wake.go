package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
)

// maxAutoWakes bounds the turns background work may start while the operator
// types nothing. Past it a wake is shown but does not run the model.
const maxAutoWakes = 5

// handleWake answers a background subagent or process monitor. A running turn
// takes it as a steer at its next step; an idle terminal starts a turn with
// it, the way the operator's own message would.
func (m *Model) handleWake(event agent.Event) (tea.Model, tea.Cmd) {
	next := waitForEvent(m.app.Events())
	text := strings.TrimSpace(event.Text)
	if text == "" {
		return m, next
	}
	headline := wakeHeadline(text)
	prompt := agent.WakePrompt(text)
	if m.running {
		m.app.Steer(prompt)
		m.blocks = append(m.blocks, block{kind: blockNotice, text: headline})
		m.refresh()
		return m, next
	}
	if m.autoWakes >= maxAutoWakes {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: headline +
			"\nNot handed to the model: background work already started several turns in a row. Send a message to continue."})
		m.refresh()
		return m, next
	}
	m.autoWakes++
	updated, cmd := m.startRunBlock(block{kind: blockNotice, text: headline}, prompt, nil)
	return updated, tea.Batch(cmd, next)
}

// wakeHeadline is the one transcript line a wake shows: its first line.
func wakeHeadline(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	return "Background: " + truncate(strings.TrimSpace(text), 160)
}

// isWakePrompt reports a queued input that is a wake, not the operator's text.
func isWakePrompt(text string) bool {
	return strings.HasPrefix(text, "<background-event>")
}
