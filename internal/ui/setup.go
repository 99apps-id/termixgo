package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// setupStep is where the onboarding wizard is.
type setupStep int

const (
	setupProvider setupStep = iota
	setupKey
	setupModel
	setupCustomModel
	setupTelegramAsk
	setupTelegramToken
	setupTelegramPair
	setupDone
)

// setupState carries the wizard between key presses.
type setupState struct {
	step        setupStep
	providerID  string
	message     string
	errText     string
	pairingCode string
	botUser     string
	summary     []string
}

// startSetup opens the wizard at the provider step.
func (m *Model) startSetup() {
	m.setup = setupState{step: setupProvider, message: "Choose the provider that will run your models."}
	m.input.Blur()
	m.input.EchoMode = textinput.EchoNormal
	m.picker = picker{title: "Setup: provider", action: "setup-provider", items: setupProviderItems()}
	m.picker.applyFilter()
	m.current = modePicker
}

// setupProviderItems lists every provider with its key requirement.
func setupProviderItems() []pickerItem {
	providers := provider.Providers()
	items := make([]pickerItem, 0, len(providers))
	for _, info := range providers {
		extra := "local"
		if info.NeedsKey {
			extra = "API key required"
		}
		items = append(items, pickerItem{ID: info.ID, Label: info.Label, Detail: info.DefaultBaseURL, Extra: extra})
	}
	return items
}

// setupModelItems lists a provider's catalogue models.
func setupModelItems(providerID string) []pickerItem {
	models := provider.ModelsFor(providerID)
	items := make([]pickerItem, 0, len(models))
	for _, model := range models {
		items = append(items, pickerItem{ID: model.ID, Label: model.Label, Detail: model.Description})
	}
	return items
}

// handleSetupKey drives the wizard's input steps.
func (m *Model) handleSetupKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" && m.setup.step == setupProvider {
		m.current = modeChat
		m.refresh()
		return m, nil
	}

	switch m.setup.step {
	case setupProvider:
		if key.String() == "enter" {
			m.picker = picker{title: "Setup: provider", action: "setup-provider", items: setupProviderItems()}
			m.picker.applyFilter()
			m.current = modePicker
		}
		return m, nil

	case setupKey:
		if key.String() == "enter" {
			return m.saveProviderKey()
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(key)
		return m, cmd

	case setupCustomModel:
		if key.String() == "enter" {
			return m.saveCustomModel()
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(key)
		return m, cmd

	case setupTelegramAsk:
		switch strings.ToLower(key.String()) {
		case "y", "enter":
			m.setup.step = setupTelegramToken
			m.setup.errText = ""
			m.input.SetValue("")
			m.input.Placeholder = "123456:ABC-DEF..."
			m.input.EchoMode = textinput.EchoPassword
			m.input.Focus()
			return m, textareaBlink()
		case "n", "esc":
			m.finishSetup("Setup complete. Run /telegram later if you want the companion bot.")
			return m, nil
		}
		return m, nil

	case setupTelegramToken:
		if key.String() == "enter" {
			return m.saveTelegramToken()
		}
		if key.String() == "esc" {
			m.finishSetup("Setup complete without Telegram.")
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(key)
		return m, cmd

	case setupTelegramPair:
		if key.String() == "enter" || key.String() == "esc" {
			m.finishSetup("Setup complete. Telegram is waiting for /pair.")
			return m, nil
		}
		return m, nil

	case setupDone:
		if key.String() == "enter" || key.String() == "esc" {
			m.current = modeChat
			m.userMessage()
			m.refresh()
		}
		return m, nil
	}
	return m, nil
}

// saveProviderKey stores the typed API key and advances to model selection.
func (m *Model) saveProviderKey() (tea.Model, tea.Cmd) {
	key := strings.TrimSpace(m.input.Value())
	if key == "" {
		m.setup.errText = "The API key is empty. Paste it and press Enter."
		return m, nil
	}
	if err := m.app.Secrets().Set(secrets.ProviderKey(m.setup.providerID), key); err != nil {
		m.setup.errText = "Could not store the key: " + err.Error()
		return m, nil
	}
	m.input.SetValue("")
	m.input.Blur()
	m.input.EchoMode = textinput.EchoNormal
	m.setup.errText = ""

	info, _ := provider.ByID(m.setup.providerID)
	items := setupModelItems(m.setup.providerID)
	if len(items) == 0 {
		m.setup.step = setupCustomModel
		m.setup.message = fmt.Sprintf("Stored a key for %s. Type the model id to use.", info.Label)
		m.input.Placeholder = "model-id"
		m.input.Focus()
		return m, textareaBlink()
	}
	m.setup.step = setupModel
	m.setup.message = fmt.Sprintf("Stored a key for %s. Pick the default model.", info.Label)
	m.picker = picker{title: "Setup: default model", action: "setup-model", items: items}
	m.picker.applyFilter()
	m.current = modePicker
	return m, nil
}

// saveCustomModel applies a hand-typed model id.
func (m *Model) saveCustomModel() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.input.Value())
	if value == "" {
		m.setup.errText = "Type a model id."
		return m, nil
	}
	qualified := value
	if !strings.Contains(value, ":") {
		qualified = m.setup.providerID + ":" + value
	}
	model, err := m.app.SetModelByQuery(qualified)
	if err != nil {
		m.setup.errText = err.Error()
		return m, nil
	}
	m.input.SetValue("")
	m.input.Blur()
	m.setup.summary = append(m.setup.summary, "model: "+model.Label)
	m.setup.step = setupTelegramAsk
	m.setup.message = "Connect the Telegram companion bot now?"
	m.setup.errText = ""
	return m, nil
}

// saveTelegramToken verifies and stores the bot token, then starts polling.
func (m *Model) saveTelegramToken() (tea.Model, tea.Cmd) {
	token := strings.TrimSpace(m.input.Value())
	if token == "" {
		m.setup.errText = "Paste the token from @BotFather."
		return m, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	username, err := m.app.VerifyTelegramToken(ctx, token)
	if err != nil {
		m.setup.errText = "Telegram rejected the token: " + err.Error()
		return m, nil
	}
	if err := m.app.SetTelegramToken(token); err != nil {
		m.setup.errText = err.Error()
		return m, nil
	}
	code, err := m.app.EnsurePairingCode()
	if err != nil {
		m.setup.errText = err.Error()
		return m, nil
	}
	m.setup.botUser = username
	m.setup.pairingCode = code
	m.input.SetValue("")
	m.input.Blur()
	m.input.EchoMode = textinput.EchoNormal
	m.setup.errText = ""
	if err := m.app.SetTelegramEnabled(true); err != nil {
		m.setup.errText = "Could not start the bot: " + err.Error()
		return m, nil
	}
	m.setup.step = setupTelegramPair
	m.setup.message = fmt.Sprintf("Open @%s in Telegram and send /pair %s", username, code)
	return m, nil
}

// finishSetup closes the wizard with a summary.
func (m *Model) finishSetup(message string) {
	if m.app.HasModel() {
		m.setup.summary = append(m.setup.summary, "model: "+m.app.ModelLabel())
	}
	m.setup.step = setupDone
	m.setup.message = message
	m.setup.errText = ""
	m.input.Blur()
	m.current = modeSetup
}

// userMessage appends the completion notice to the transcript.
func (m *Model) userMessage() {
	lines := []string{"Onboarding complete."}
	if len(m.setup.summary) > 0 {
		lines = append(lines, m.setup.summary...)
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: strings.Join(lines, "\n")})
}

// viewSetup renders the wizard.
func (m *Model) viewSetup() string {
	body := []string{
		m.styles.BoxTitle.Render(fmt.Sprintf("Setup (%d/%d)", setupProgress(m.setup.step), 4)),
		"",
	}
	if m.setup.message != "" {
		body = append(body, m.styles.StatusValue.Render(m.setup.message), "")
	}

	switch m.setup.step {
	case setupProvider, setupModel:
		body = append(body, m.styles.Dim.Render("Choose from the list."))
	case setupKey:
		body = append(body, m.styles.Dim.Render("Paste the API key. It is stored in ~/.termixgo/secrets.json with mode 0600 and never shown again."))
		body = append(body, "", m.input.View())
	case setupCustomModel:
		body = append(body, m.styles.Dim.Render("Type the model id your server expects."))
		body = append(body, "", m.input.View())
	case setupTelegramAsk:
		body = append(body, m.styles.MenuKey.Render("y")+" yes   "+m.styles.MenuKey.Render("n")+" not now")
	case setupTelegramToken:
		body = append(body, m.styles.Dim.Render("Create a bot with @BotFather, then paste its token."))
		body = append(body, "", m.input.View())
	case setupTelegramPair:
		body = append(body, m.styles.StatusValue.Render(m.setup.pairingCode))
		body = append(body, "", m.styles.Dim.Render("The bot is polling already. After you send /pair, press Enter."))
	case setupDone:
		for _, line := range m.setup.summary {
			body = append(body, m.styles.Dim.Render("  "+line))
		}
		body = append(body, "", m.styles.Hint.Render("Press Enter to continue."))
	}

	if m.setup.errText != "" {
		body = append(body, "", m.styles.Error.Render(m.setup.errText))
	}
	body = append(body, "", m.styles.Hint.Render("Esc cancels"))
	return m.styles.Box.Width(m.width - 4).Render(strings.Join(body, "\n"))
}

// setupProgress maps a step to the visible progress counter.
func setupProgress(step setupStep) int {
	switch step {
	case setupProvider:
		return 1
	case setupKey, setupModel, setupCustomModel:
		return 2
	case setupTelegramAsk, setupTelegramToken, setupTelegramPair:
		return 3
	default:
		return 4
	}
}

// textareaBlink restarts the cursor blink for a focused input.
func textareaBlink() tea.Cmd { return textinput.Blink }
