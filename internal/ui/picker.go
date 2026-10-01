package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/provider"
)

// pickerItem is one selectable row.
type pickerItem struct {
	ID     string
	Label  string
	Detail string
	Extra  string
}

// picker is a filterable list overlay.
type picker struct {
	title   string
	action  string
	items   []pickerItem
	visible []pickerItem
	cursor  int
	filter  string
}

func newPicker() picker { return picker{} }

// openPicker shows the overlay.
func (m *Model) openPicker(title, action string, items []pickerItem) {
	m.picker = picker{title: title, action: action, items: items}
	m.picker.applyFilter()
	m.current = modePicker
	m.refresh()
}

// applyFilter recomputes the visible rows from the filter text.
func (p *picker) applyFilter() {
	needle := strings.ToLower(strings.TrimSpace(p.filter))
	if needle == "" {
		p.visible = p.items
	} else {
		filtered := make([]pickerItem, 0, len(p.items))
		for _, item := range p.items {
			haystack := strings.ToLower(item.ID + " " + item.Label + " " + item.Detail)
			if strings.Contains(haystack, needle) {
				filtered = append(filtered, item)
			}
		}
		p.visible = filtered
	}
	if p.cursor >= len(p.visible) {
		p.cursor = max(0, len(p.visible)-1)
	}
}

func (p *picker) selected() (pickerItem, bool) {
	if len(p.visible) == 0 {
		return pickerItem{}, false
	}
	return p.visible[p.cursor], true
}

func (m *Model) handlePickerKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		if strings.HasPrefix(m.picker.action, "setup-") {
			m.current = modeSetup
			m.picker = picker{}
			return m, nil
		}
		if strings.HasPrefix(m.picker.action, slashArgAction) {
			resume := m.pickerResume
			pending := m.slashPending
			m.picker = picker{}
			m.pickerResume = ""
			m.slashPending = ""
			m.composer.SetValue(resume)
			if pending != "" {
				m.slashOpen = true
				m.slashAnchor = resume
				m.slashInput = pending
				m.refreshSlashMenu()
			} else {
				m.updateSlashMatches()
			}
			return m, m.enterChat()
		}
		m.picker = picker{}
		return m, m.enterChat()
	case "up", "ctrl+p":
		m.picker.cursor = (m.picker.cursor - 1 + max(1, len(m.picker.visible))) % max(1, len(m.picker.visible))
		return m, nil
	case "down", "ctrl+n":
		m.picker.cursor = (m.picker.cursor + 1) % max(1, len(m.picker.visible))
		return m, nil
	case "backspace":
		if m.picker.filter != "" {
			m.picker.filter = trimLastRune(m.picker.filter)
			m.picker.applyFilter()
		}
		return m, nil
	case "enter":
		item, ok := m.picker.selected()
		if !ok {
			return m, nil
		}
		action := m.picker.action
		m.current = modeChat
		m.picker = picker{}
		return m.applyPickerChoice(action, item)
	}
	if key.Type == tea.KeyRunes {
		m.picker.filter += string(key.Runes)
		m.picker.applyFilter()
	}
	return m, nil
}

// applyPickerChoice runs the action bound to the open picker.
func (m *Model) applyPickerChoice(action string, item pickerItem) (tea.Model, tea.Cmd) {
	if name, ok := strings.CutPrefix(action, slashArgAction); ok {
		return m.applySlashArg(name, item.ID)
	}
	switch action {
	case "model":
		model, err := m.app.SetModelByQuery(item.ID)
		if err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
		} else {
			m.refreshWelcome()
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "Model is now " + model.Label})
		}
		return m, m.enterChat()
	case "session":
		if m.running {
			m.blocks = append(m.blocks, block{kind: blockError, text: "Wait for the current turn to finish before resuming a session."})
			return m, m.enterChat()
		}
		session, err := agent.LoadSession(item.ID)
		if err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: err.Error()})
			return m, m.enterChat()
		}
		m.app.LoadSession(session)
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("Resumed session %s (%d turns)", shortID(session.ID()), session.Turns())})
		return m, m.enterChat()
	case "setup-provider":
		m.setup.providerID = item.ID
		m.setup.message = ""
		m.setup.errText = ""
		if provider.NeedsEndpoint(item.ID) {
			// A provider with no default host cannot be reached at all until
			// the operator says where its server lives.
			m.current = modeSetup
			m.setup.step = setupEndpoint
			m.setup.message = fmt.Sprintf("Where does %s live?", item.Label)
			m.input.SetValue("")
			m.input.Placeholder = "https://your-server/v1"
			m.input.EchoMode = textinput.EchoNormal
			m.input.Focus()
			m.refresh()
			return m, textareaBlink()
		}
		if provider.UsesOAuth(item.ID) {
			// A login provider must not be asked for an API key: point the
			// operator at the device login and verify it before continuing.
			m.current = modeSetup
			m.setup.step = setupOAuth
			m.setup.message = fmt.Sprintf("%s logs in with a device code.", item.Label)
			m.setup.errText = ""
			m.refresh()
			return m, nil
		}
		if !providerNeedsKey(item.ID) {
			// A local server needs no key: go straight to model selection.
			m.setup.step = setupModel
			m.openPicker(setupPickerTitle("Model"), "setup-model", setupModelItems(item.ID))
			return m, nil
		}
		info, _ := provider.ByID(item.ID)
		m.current = modeSetup
		m.setup.step = setupKey
		m.setup.message = fmt.Sprintf("Paste the %s API key.", info.Label)
		m.input.SetValue("")
		m.input.Placeholder = keyPlaceholder(info, false)
		m.input.EchoMode = textinput.EchoPassword
		m.input.Focus()
		m.refresh()
		return m, textareaBlink()
	case "setup-model":
		model, err := m.app.SetModelByQuery(item.ID)
		if err != nil {
			m.setup.errText = err.Error()
			m.current = modeSetup
			return m, nil
		}
		m.refreshWelcome()
		m.setup.summary = append(m.setup.summary, "model: "+model.Label)
		m.setup.step = setupTelegramAsk
		m.setup.message = "Connect the Telegram companion bot now?"
		m.setup.errText = ""
		m.current = modeSetup
		m.refresh()
		return m, nil
	}
	return m, m.enterChat()
}

func (m *Model) viewPicker() string {
	title := m.picker.title
	var body []string
	body = append(body, m.styles.BoxTitle.Render(title))
	body = append(body, "")
	if m.picker.filter != "" {
		body = append(body, m.styles.Dim.Render("filter: "+m.picker.filter))
	}
	// The list adapts to the window: the header, the hint and the box frame
	// keep their rows and the rest belongs to the items, so a list screen never
	// grows past the terminal and shifts the frame.
	windowSize := m.menuRowLimit()
	start := 0
	if m.picker.cursor >= windowSize {
		start = m.picker.cursor - windowSize + 1
	}
	end := start + windowSize
	if end > len(m.picker.visible) {
		end = len(m.picker.visible)
	}
	for index := start; index < end; index++ {
		item := m.picker.visible[index]
		line := item.Label
		if item.Detail != "" {
			line += "  " + m.styles.MenuDesc.Render(item.Detail)
		}
		if item.Extra != "" {
			line += "  " + m.styles.Plan.Render(item.Extra)
		}
		if index == m.picker.cursor {
			body = append(body, m.styles.MenuSelected.Render("> ")+line)
			continue
		}
		body = append(body, "  "+line)
	}
	if len(m.picker.visible) == 0 {
		body = append(body, m.styles.Dim.Render("  No matches."))
	}
	body = append(body, "", m.styles.Hint.Render("Type to filter | Up/Down move | Enter select | Esc cancel"))
	// Clip to the box's wrap width before rendering, and bound the rows as a
	// second guard, so the frame stays inside the terminal.
	width := max(1, m.width-8)
	for index := range body {
		body[index] = truncate(body[index], width)
	}
	body = fitRows(body, m.height-4, m.styles.Dim)
	return m.styles.Box.Width(m.width - 4).Render(strings.Join(body, "\n"))
}

// providerNeedsKey reports whether a provider requires an API key.
func providerNeedsKey(id string) bool {
	info, ok := provider.ByID(id)
	if !ok {
		return true
	}
	return info.NeedsKey
}
