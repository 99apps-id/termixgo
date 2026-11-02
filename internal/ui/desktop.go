package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/desktop"
)

// slashDesktop turns local desktop control on or off. The setting is saved, so
// the next launch keeps it, and the live registry is rebuilt for the next
// turn. It is off by default because the tools act with the operator's own
// permissions.
func (m *Model) slashDesktop(args string) (tea.Model, tea.Cmd) {
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "", "status":
		m.blocks = append(m.blocks, block{kind: blockNotice, text: desktopStatus(m.appConfig().DesktopControl)})
	case "on", "yes", "true", "enable":
		if err := m.app.SetDesktopControl(true); err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Cannot enable desktop control: " + err.Error()})
			break
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: desktopStatus(true)})
	case "off", "no", "false", "disable":
		if err := m.app.SetDesktopControl(false); err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Cannot disable desktop control: " + err.Error()})
			break
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: desktopStatus(false)})
	default:
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Use /desktop, /desktop on or /desktop off."})
	}
	m.refresh()
	return m, nil
}

// desktopStatus describes the switch and what the host is still missing, so an
// enabled surface that cannot work never looks ready.
func desktopStatus(enabled bool) string {
	state := "off"
	if enabled {
		state = "on"
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "Desktop control is %s.", state)
	if enabled {
		builder.WriteString("\nThe agent can capture the screen, move and click the pointer, type, press keys, focus and launch applications. Every call is approval gated, and a guest chat never receives these tools.")
	} else {
		builder.WriteString("\nTurn it on with /desktop on. The tools then appear in the next turn's registry.")
	}
	if missing := desktop.Requirements(); len(missing) > 0 {
		fmt.Fprintf(&builder, "\nThis host is missing: %s.", strings.Join(missing, ", "))
	}
	return builder.String()
}
