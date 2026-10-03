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
	"github.com/99apps-id/termixgo/internal/version"
)

// setupStep is where the onboarding wizard is.
type setupStep int

const (
	setupProvider setupStep = iota
	// setupEndpoint asks where a provider's server lives. It only appears for a
	// provider with no default host, whose address carries an account id or
	// points at the operator's own machine.
	setupEndpoint
	setupKey
	setupModel
	setupCustomModel
	setupTelegramAsk
	setupTelegramToken
	setupTelegramPair
	setupDone
	// setupOAuth is the login step for a provider that uses a device code
	// instead of an API key. It is last so the numeric order of the others is
	// unchanged.
	setupOAuth
	setupVoiceModel
	setupCustomVoiceModel
	setupImageModel
	setupCustomImageModel
)

// setupTitlePrefix brands every wizard title with the plain wordmark and the
// version of the running build, so the picker and the wizard steps never show
// two different headers and a screenshot of the setup names its own version.
var setupTitlePrefix = version.Name + " " + version.Version + " Setup"

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

// setupPickerTitle names the picker overlay for a wizard step.
func setupPickerTitle(step string) string { return setupTitlePrefix + ": " + step }

// startSetup opens the wizard at the provider step.
func (m *Model) startSetup() {
	m.setup = setupState{step: setupProvider, message: "Choose the provider that will run your models."}
	m.input.Blur()
	m.input.EchoMode = textinput.EchoNormal
	m.composer.Blur()
	m.picker = picker{title: setupPickerTitle("Provider"), action: "setup-provider", items: setupProviderItems()}
	m.picker.applyFilter()
	m.current = modePicker
}

// setupProviderItems lists every provider with its key requirement.
func setupProviderItems() []pickerItem {
	providers := provider.Providers()
	items := make([]pickerItem, 0, len(providers))
	for _, info := range providers {
		extra := "local"
		switch {
		case info.OAuth:
			extra = "login required"
		case info.NeedsKey:
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

// setupVoiceItems lists available voice models for the wizard.
func setupVoiceItems() []pickerItem {
	return []pickerItem{
		{ID: "default", Label: "Default Whisper", Detail: "Auto-detect Groq or OpenAI Whisper"},
		{ID: "groq:whisper-large-v3-turbo", Label: "Groq Whisper Large v3 Turbo", Detail: "Ultra-fast speech transcription"},
		{ID: "openai:whisper-1", Label: "OpenAI Whisper-1", Detail: "Standard OpenAI speech-to-text"},
		{ID: "custom", Label: "Custom Model", Detail: "Type a custom model id"},
		{ID: "skip", Label: "Skip", Detail: "Do not set a voice model"},
	}
}

// setupImageItems lists available image creation models for the wizard.
func setupImageItems() []pickerItem {
	return []pickerItem{
		{ID: "dall-e-3", Label: "OpenAI DALL-E 3", Detail: "High-quality image generation"},
		{ID: "openai:gpt-4o", Label: "OpenAI GPT-4o", Detail: "Multimodal visual and image reasoning"},
		{ID: "gemini-2.5-flash", Label: "Google Gemini 2.5 Flash", Detail: "Fast multimodal visual model"},
		{ID: "qwen/qwen3.8-27b", Label: "Qwen 3.8 27B Vision", Detail: "Open vision-language model"},
		{ID: "custom", Label: "Custom Model", Detail: "Type a custom model id"},
		{ID: "skip", Label: "Skip", Detail: "Use active model for subagents"},
	}
}

// handleSetupKey drives the wizard's input steps.
func (m *Model) handleSetupKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Escape is a way back, never a trap. At the provider list it leaves the
	// wizard; on a provider sub-step it returns to the list so the operator can
	// choose another provider instead of being stuck on an input or login step.
	if key.String() == "esc" {
		switch m.setup.step {
		case setupProvider:
			return m, m.enterChat()
		case setupEndpoint, setupKey, setupOAuth, setupCustomModel:
			m.stepBackToProvider()
			return m, nil
		}
	}

	switch m.setup.step {
	case setupProvider:
		if key.String() == "enter" {
			m.picker = picker{title: setupPickerTitle("Provider"), action: "setup-provider", items: setupProviderItems()}
			m.picker.applyFilter()
			m.current = modePicker
		}
		return m, nil

	case setupEndpoint:
		if key.String() == "enter" {
			return m.saveEndpoint()
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(key)
		return m, cmd

	case setupKey:
		if key.String() == "enter" {
			return m.saveProviderKey()
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(key)
		return m, cmd

	case setupOAuth:
		if key.String() == "enter" {
			return m.confirmOAuthLogin()
		}
		return m, nil

	case setupCustomModel:
		if key.String() == "enter" {
			return m.saveCustomModel()
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(key)
		return m, cmd

	case setupCustomVoiceModel:
		if key.String() == "enter" {
			val := strings.TrimSpace(m.input.Value())
			if val != "" {
				_ = m.app.SetVoiceModel(val)
				m.setup.summary = append(m.setup.summary, "voice: "+val)
			}
			return m.advanceToImageModel()
		}
		if key.String() == "esc" {
			return m.advanceToImageModel()
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(key)
		return m, cmd

	case setupCustomImageModel:
		if key.String() == "enter" {
			val := strings.TrimSpace(m.input.Value())
			if val != "" {
				_ = m.app.SetImageModel(val)
				m.setup.summary = append(m.setup.summary, "image: "+val)
			}
			return m.advanceToTelegramAsk()
		}
		if key.String() == "esc" {
			return m.advanceToTelegramAsk()
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
			m.userMessage()
			return m, m.enterChat()
		}
		return m, nil
	}
	return m, nil
}

// saveEndpoint validates and stores the typed base URL, then offers the key
// step. A self-hosted compatible server often wants a key even though the
// catalogue cannot know it, so the key is always offered and an empty answer
// means none.
func (m *Model) saveEndpoint() (tea.Model, tea.Cmd) {
	endpoint, err := provider.NormalizeBaseURL(m.input.Value())
	if err != nil {
		m.setup.errText = err.Error()
		return m, nil
	}
	if err := m.app.SetBaseURL(m.setup.providerID, endpoint); err != nil {
		m.setup.errText = err.Error()
		return m, nil
	}
	info, _ := provider.ByID(m.setup.providerID)
	m.input.SetValue("")
	m.input.Blur()
	m.setup.errText = ""
	m.setup.summary = append(m.setup.summary, "endpoint: "+endpoint)
	m.setup.step = setupKey
	m.setup.message = fmt.Sprintf("Endpoint set for %s. Paste an API key, or press Enter to skip it.", info.Label)
	m.input.Placeholder = keyPlaceholder(info, true)
	m.input.EchoMode = textinput.EchoPassword
	m.input.Focus()
	m.current = modeSetup
	return m, textareaBlink()
}

// stepBackToProvider returns a provider sub-step to the provider list, so the
// operator can pick another provider. It keeps the wizard open, which is why
// Escape here is a step back rather than an exit.
func (m *Model) stepBackToProvider() {
	m.setup.step = setupProvider
	m.setup.errText = ""
	m.setup.message = "Choose the provider that will run your models."
	m.input.SetValue("")
	m.input.Blur()
	m.input.EchoMode = textinput.EchoNormal
	m.picker = picker{title: setupPickerTitle("Provider"), action: "setup-provider", items: setupProviderItems()}
	m.picker.applyFilter()
	m.current = modePicker
}

// confirmOAuthLogin checks that a login exists before moving on. The login
// itself is a device flow run from a shell, because it needs to show a code and
// poll; the wizard only verifies the result rather than asking for a key.
func (m *Model) confirmOAuthLogin() (tea.Model, tea.Cmd) {
	if !provider.HasKey(m.app.Secrets(), m.setup.providerID) {
		m.setup.errText = fmt.Sprintf("No login found. Run 'termixgo login %s' in a shell, then press Enter.", m.setup.providerID)
		return m, nil
	}
	m.setup.errText = ""
	m.setup.message = "Logged in."
	m.setup.summary = append(m.setup.summary, "login: "+m.setup.providerID)
	return m.advanceToModel()
}

// saveProviderKey stores the typed API key and advances to model selection.
// An empty answer is only allowed when the provider does not require one, so a
// compatible server can be configured without a key.
func (m *Model) saveProviderKey() (tea.Model, tea.Cmd) {
	key := strings.TrimSpace(m.input.Value())
	info, _ := provider.ByID(m.setup.providerID)
	if key == "" {
		if info.NeedsKey {
			m.setup.errText = "The API key is empty. Paste it and press Enter."
			return m, nil
		}
		m.input.SetValue("")
		m.input.Blur()
		m.input.EchoMode = textinput.EchoNormal
		m.setup.errText = ""
		m.setup.message = fmt.Sprintf("Continuing for %s without a key.", info.Label)
		return m.advanceToModel()
	}
	if err := m.app.Secrets().Set(secrets.ProviderKey(m.setup.providerID), key); err != nil {
		m.setup.errText = "Could not store the key: " + err.Error()
		return m, nil
	}
	m.input.SetValue("")
	m.input.Blur()
	m.input.EchoMode = textinput.EchoNormal
	m.setup.errText = ""
	m.setup.message = fmt.Sprintf("Stored a key for %s.", info.Label)
	return m.advanceToModel()
}

// advanceToModel moves past the key step: a provider with catalogue models
// opens the picker, while one the catalogue cannot know asks for the id by hand.
func (m *Model) advanceToModel() (tea.Model, tea.Cmd) {
	items := setupModelItems(m.setup.providerID)
	if len(items) == 0 {
		m.setup.step = setupCustomModel
		m.input.Placeholder = "model-id"
		m.input.EchoMode = textinput.EchoNormal
		m.input.Focus()
		m.current = modeSetup
		return m, textareaBlink()
	}
	m.setup.step = setupModel
	m.picker = picker{title: setupPickerTitle("Model"), action: "setup-model", items: items}
	m.picker.applyFilter()
	m.current = modePicker
	return m, nil
}

// keyPlaceholder renders the hint for the key field. A provider that does not
// require a key says so, because an invisible empty-Enter shortcut is not
// something an operator can discover.
func keyPlaceholder(info provider.Provider, optional bool) string {
	if optional || !info.NeedsKey {
		return "API key (Enter skips)"
	}
	if info.KeyPrefix != "" {
		return "API key, usually starts with " + info.KeyPrefix
	}
	return "API key"
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
	m.refreshWelcome()
	m.setup.summary = append(m.setup.summary, "model: "+model.Label)
	return m.advanceToVoiceModel()
}

func (m *Model) advanceToVoiceModel() (tea.Model, tea.Cmd) {
	m.setup.errText = ""
	m.setup.step = setupVoiceModel
	m.openPicker(setupPickerTitle("Voice Model"), "setup-voice-model", setupVoiceItems())
	return m, nil
}

func (m *Model) advanceToImageModel() (tea.Model, tea.Cmd) {
	m.setup.errText = ""
	m.setup.step = setupImageModel
	m.openPicker(setupPickerTitle("Image Model"), "setup-image-model", setupImageItems())
	return m, nil
}

func (m *Model) advanceToTelegramAsk() (tea.Model, tea.Cmd) {
	m.input.SetValue("")
	m.input.Blur()
	m.input.EchoMode = textinput.EchoNormal
	m.setup.step = setupTelegramAsk
	m.setup.message = "Connect the Telegram companion bot now?"
	m.setup.errText = ""
	m.current = modeSetup
	m.refresh()
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
		m.styles.BoxTitle.Render(fmt.Sprintf("%s (%d/%d)", setupTitlePrefix, setupProgress(m.setup.step), 4)),
		"",
	}
	if m.setup.message != "" {
		body = append(body, m.styles.StatusValue.Render(m.setup.message), "")
	}

	switch m.setup.step {
	case setupProvider, setupModel, setupVoiceModel, setupImageModel:
		body = append(body, m.styles.Dim.Render("Choose from the list."))
	case setupEndpoint:
		body = append(body, m.styles.Dim.Render("Type the server base URL. It is saved in the settings file and used for every request to this provider."))
		body = append(body, "", m.input.View())
	case setupKey:
		body = append(body, m.styles.Dim.Render("Paste the API key. It is stored in ~/.termixgo/secrets.json with mode 0600 and never shown again."))
		body = append(body, "", m.input.View())
	case setupOAuth:
		body = append(body, m.styles.Dim.Render("This provider logs in with a device code, not an API key."))
		body = append(body, m.styles.Dim.Render("In a shell run: termixgo login "+m.setup.providerID))
		body = append(body, "", m.styles.Hint.Render("Press Enter once the login finishes."))
	case setupCustomModel, setupCustomVoiceModel, setupCustomImageModel:
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
	// The box wraps at its Width minus the horizontal padding, so a line wider
	// than that wraps and grows the page past the terminal; the rendering then
	// drops the top rows and shifts everything. Clip first, then bound the row
	// count as a second guard.
	width := max(1, m.width-8)
	for index := range body {
		body[index] = truncate(body[index], width)
	}
	body = fitRows(body, m.height-4, m.styles.Dim)
	return m.styles.Box.Width(m.width - 4).Render(strings.Join(body, "\n"))
}

// setupProgress maps a step to the visible progress counter.
func setupProgress(step setupStep) int {
	switch step {
	case setupProvider:
		return 1
	case setupEndpoint, setupKey, setupModel, setupCustomModel, setupOAuth:
		return 2
	case setupVoiceModel, setupCustomVoiceModel, setupImageModel, setupCustomImageModel:
		return 3
	case setupTelegramAsk, setupTelegramToken, setupTelegramPair:
		return 3
	default:
		return 4
	}
}

// textareaBlink restarts the cursor blink for a focused input.
func textareaBlink() tea.Cmd { return textinput.Blink }
