package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/app"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// setupModel builds a model for an install that has no model yet, which is
// what makes the onboarding wizard open on its own.
//
// It is the opposite fixture to chatModel: that one is already configured so
// the chat loop can be reached, this one must not be.
func wizardModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())

	application, err := app.New(t.TempDir())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	model := New(application)
	if model.current != modePicker || model.setup.step != setupProvider {
		t.Fatalf("a fresh install should open the wizard on the provider step, got mode %d step %d", model.current, model.setup.step)
	}
	if len(model.picker.visible) == 0 {
		t.Fatalf("the provider picker should list something")
	}
	resize(model, 120, 40)
	return model
}

// choose filters the open picker to one entry and selects it.
func choose(t *testing.T, model *Model, filter string) *Model {
	t.Helper()
	filtered := press(t, model, filter)
	if len(filtered.picker.visible) != 1 {
		t.Fatalf("filter %q matched %d entries, want exactly one", filter, len(filtered.picker.visible))
	}
	return press(t, filtered, "enter")
}

// TestWizardWalksALocalProviderEndToEnd is the first-run path most people
// take: a local server needs no key, so provider and model are all that is
// asked before the wizard offers Telegram.
func TestWizardWalksALocalProviderEndToEnd(t *testing.T) {
	model := wizardModel(t)

	atKey := choose(t, model, "ollama")
	if atKey.setup.step != setupModel {
		t.Fatalf("a local provider needs no key, so the wizard should skip to the model: step %d", atKey.setup.step)
	}
	if atKey.setup.providerID != "ollama" {
		t.Errorf("provider = %q", atKey.setup.providerID)
	}

	atTelegram := choose(t, atKey, "qwen2.5-coder")
	if atTelegram.setup.step != setupTelegramAsk {
		t.Fatalf("after a model the wizard offers Telegram: step %d", atTelegram.setup.step)
	}
	if !atTelegram.app.HasModel() {
		t.Fatalf("the model should be applied by now")
	}
	if !strings.Contains(atTelegram.setup.message, "Telegram") {
		t.Errorf("message = %q", atTelegram.setup.message)
	}

	declined := press(t, atTelegram, "n")
	if declined.setup.step != setupDone || declined.current != modeSetup {
		t.Fatalf("declining Telegram should finish the wizard: step %d mode %d", declined.setup.step, declined.current)
	}

	closed := press(t, declined, "enter")
	if closed.current != modeChat {
		t.Errorf("Enter on the summary should return to chat, got mode %d", closed.current)
	}
	view := display(closed)
	if !strings.Contains(view, "Onboarding complete") {
		t.Errorf("the transcript should record the result:\n%s", view)
	}
	if !strings.Contains(view, "model:") {
		t.Errorf("the summary should name the chosen model:\n%s", view)
	}
}

// TestWizardStoresAProviderKey walks the keyed path and checks the key lands
// in the secret store rather than the settings file.
func TestWizardStoresAProviderKey(t *testing.T) {
	model := wizardModel(t)

	atKey := choose(t, model, "anthropic")
	if atKey.setup.step != setupKey {
		t.Fatalf("a keyed provider must ask for a key: step %d", atKey.setup.step)
	}
	if atKey.current != modeSetup {
		t.Errorf("the wizard should own the screen, got mode %d", atKey.current)
	}
	if !atKey.input.Focused() {
		t.Errorf("the key field should be focused so typing works")
	}

	// An empty key must be refused with a message, not stored.
	refused := press(t, atKey, "enter")
	if refused.setup.errText == "" {
		t.Fatalf("an empty key must be reported")
	}
	if refused.app.Secrets().Has(secrets.ProviderKey("anthropic")) {
		t.Errorf("an empty key must not be stored")
	}
	if refused.setup.step != setupKey {
		t.Errorf("the wizard must stay on the key step")
	}

	refused.input.SetValue("sk-ant-test")
	stored := press(t, refused, "enter")
	if stored.setup.step != setupModel {
		t.Fatalf("a stored key should advance to model selection: step %d", stored.setup.step)
	}
	if got := stored.app.Secrets().Get(secrets.ProviderKey("anthropic")); got != "sk-ant-test" {
		t.Errorf("stored key = %q", got)
	}
	// The key must not leak into the non-secret settings file.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if strings.Contains(strings.Join(cfg.AlwaysAllowedTools, " "), "sk-ant-test") {
		t.Errorf("the key ended up in the settings file")
	}
	// The field must go back to plain echo, or the next answer typed into a
	// shared input would still be masked.
	if stored.input.EchoMode != textinput.EchoNormal {
		t.Errorf("echo mode = %v, want it restored after the key step", stored.input.EchoMode)
	}
}

// TestWizardAsksForACustomModel covers a provider with nothing in the local
// catalogue: the operator has to type the model id.
func TestWizardAsksForACustomModel(t *testing.T) {
	model := wizardModel(t)

	atKey := choose(t, model, "zhipu")
	if atKey.setup.step != setupKey {
		t.Fatalf("zhipu needs a key: step %d", atKey.setup.step)
	}
	atKey.input.SetValue("zhipu-key")
	atCustom := press(t, atKey, "enter")
	if atCustom.setup.step != setupCustomModel {
		t.Fatalf("a provider with no catalogue models must ask for one: step %d", atCustom.setup.step)
	}
	if !atCustom.input.Focused() {
		t.Errorf("the model field should be focused")
	}

	// An empty model id must be refused.
	refused := press(t, atCustom, "enter")
	if refused.setup.errText == "" {
		t.Fatalf("an empty model id must be reported")
	}

	// A bare id is qualified with the chosen provider, which is what makes it
	// resolvable later.
	refused.input.SetValue("glm-4.6")
	done := press(t, refused, "enter")
	if done.setup.errText != "" {
		t.Fatalf("a typed model should be accepted, got %q", done.setup.errText)
	}
	if done.setup.step != setupTelegramAsk {
		t.Fatalf("step = %d, want the Telegram question", done.setup.step)
	}
	if done.app.CurrentModel().ID != "glm-4.6" || done.app.CurrentModel().Provider != "zhipu" {
		t.Errorf("model = %+v, want zhipu:glm-4.6", done.app.CurrentModel())
	}
	if !strings.Contains(strings.Join(done.setup.summary, " "), "glm-4.6") {
		t.Errorf("summary = %v", done.setup.summary)
	}
}

// TestWizardTelegramStepsDeclineAndEscape covers every way out of the Telegram
// part. The accepting path is deliberately not tested: it calls the real Bot
// API, which a unit test has no business doing.
func TestWizardTelegramStepsDeclineAndEscape(t *testing.T) {
	model := wizardModel(t)
	atAsk := choose(t, choose(t, model, "ollama"), "qwen2.5-coder")

	// y moves to the token step and masks the field.
	atToken := press(t, atAsk, "y")
	if atToken.setup.step != setupTelegramToken {
		t.Fatalf("y should advance to the token step: step %d", atToken.setup.step)
	}
	if !atToken.input.Focused() {
		t.Errorf("the token field should be focused")
	}
	if atToken.input.EchoMode != textinput.EchoPassword {
		t.Errorf("a bot token must be masked while typing, got %v", atToken.input.EchoMode)
	}

	// Enter with nothing typed is refused.
	refused := press(t, atToken, "enter")
	if refused.setup.errText == "" {
		t.Fatalf("an empty token must be reported")
	}
	if refused.app.TelegramToken() != "" {
		t.Errorf("an empty token must not be stored")
	}

	// Esc leaves the wizard without a bot.
	skipped := press(t, refused, "esc")
	if skipped.setup.step != setupDone {
		t.Fatalf("esc should finish the setup: step %d", skipped.setup.step)
	}
	if !strings.Contains(skipped.setup.message, "without Telegram") {
		t.Errorf("message = %q", skipped.setup.message)
	}
	if skipped.app.TelegramToken() != "" {
		t.Errorf("no token should have been stored")
	}
}

// TestWizardProviderStepIgnoresOtherKeys pins that the wizard does not fall
// through to the composer while the picker is open.
func TestWizardProviderStepIgnoresOtherKeys(t *testing.T) {
	model := wizardModel(t)
	model.current = modeSetup

	still := press(t, model, "x")
	if still.setup.step != setupProvider {
		t.Errorf("step = %d, want the provider step", still.setup.step)
	}
	// Enter on the provider step is what reopens the picker.
	reopened := press(t, still, "enter")
	if reopened.current != modePicker {
		t.Errorf("Enter should reopen the provider list, got mode %d", reopened.current)
	}
}

func TestWizardEscClosesFromTheFirstStep(t *testing.T) {
	model := wizardModel(t)
	// The wizard opens on the provider picker, so one escape goes back to the
	// wizard and the second one leaves it.
	back := press(t, model, "esc")
	if back.current != modeSetup || back.setup.step != setupProvider {
		t.Fatalf("escape should return to the wizard, got mode %d step %d", back.current, back.setup.step)
	}
	closed := press(t, back, "esc")
	if closed.current != modeChat {
		t.Errorf("the second escape should return to chat, got mode %d", closed.current)
	}
	if !closed.app.NeedsSetup() {
		t.Errorf("escaping must not pretend the setup happened")
	}
}

// TestWizardEscFromTheKeyStepIsNotAnExit is the safety rule: escape in the
// middle of the wizard must not silently abandon a stored key, so it is left
// to the later steps where there is a defined meaning.
func TestWizardEscFromTheKeyStepIsNotAnExit(t *testing.T) {
	model := wizardModel(t)
	atKey := choose(t, model, "anthropic")

	stuck := press(t, atKey, "esc")
	if stuck.setup.step != setupKey {
		t.Errorf("step = %d, want the wizard to stay on the key step", stuck.setup.step)
	}
	if stuck.app.Secrets().Has(secrets.ProviderKey("anthropic")) {
		t.Errorf("nothing should have been stored")
	}
}

func TestWizardPairStepClosesOnEitherKey(t *testing.T) {
	for _, name := range []string{"enter", "esc"} {
		t.Run(name, func(t *testing.T) {
			model := wizardModel(t)
			model.current = modeSetup
			model.setup.step = setupTelegramPair
			model.setup.pairingCode = "123456"

			if !strings.Contains(display(model), "123456") {
				t.Errorf("the pairing code must be visible while waiting:\n%s", display(model))
			}
			closed := press(t, model, name)
			if closed.setup.step != setupDone {
				t.Errorf("step = %d, want done", closed.setup.step)
			}
		})
	}
}

// TestWizardRendersEveryStep is a smoke test over the templates: a step that
// renders nothing would look like a frozen screen to the operator.
func TestWizardRendersEveryStep(t *testing.T) {
	steps := []setupStep{
		setupProvider, setupKey, setupModel, setupCustomModel,
		setupTelegramAsk, setupTelegramToken, setupTelegramPair, setupDone,
	}
	for _, step := range steps {
		name := fmt.Sprintf("step-%d", step)
		t.Run(name, func(t *testing.T) {
			model := wizardModel(t)
			model.current = modeSetup
			model.setup.step = step
			model.setup.message = "a message"
			model.setup.summary = []string{"provider: Ollama"}
			model.setup.pairingCode = "654321"
			model.setup.errText = "a problem"

			view := display(model)
			if strings.TrimSpace(view) == "" {
				t.Fatalf("step %d rendered nothing", step)
			}
			if !strings.Contains(view, "Setup (") {
				t.Errorf("the wizard header is missing at step %d:\n%s", step, view)
			}
			if !strings.Contains(view, "a message") {
				t.Errorf("the step message is missing at step %d:\n%s", step, view)
			}
		})
	}
}

// TestWizardShowsTheKeyRequirementInTheProviderList pins the hint that tells
// the operator which entries will stop to ask for a key.
func TestWizardShowsTheKeyRequirementInTheProviderList(t *testing.T) {
	model := wizardModel(t)
	view := display(model)
	if !strings.Contains(view, "API key required") {
		t.Errorf("the provider list should say which entries need a key:\n%s", view)
	}
	if !strings.Contains(view, "local") {
		t.Errorf("the provider list should say which entries are local:\n%s", view)
	}
}

// TestPickerEscapeFromASetupPickerReturnsToTheWizard guards the one branch
// that treats a setup picker differently from an ordinary one: backing out of
// a list inside the wizard must not throw the wizard away.
func TestPickerEscapeFromASetupPickerReturnsToTheWizard(t *testing.T) {
	model := wizardModel(t)
	model.setup.step = setupModel
	model.picker.action = "setup-model"
	model.current = modePicker

	returned := press(t, model, "esc")
	if returned.current != modeSetup {
		t.Errorf("mode = %d, want the wizard", returned.current)
	}
	if returned.setup.step != setupModel {
		t.Errorf("step = %d, want it kept", returned.setup.step)
	}
}

// TestChatPickerEscapeReturnsToChat is the other half of that branch.
func TestChatPickerEscapeReturnsToChat(t *testing.T) {
	model := chatModel(t)
	model.openPicker("Models", "model", setupModelItems("ollama"))
	if model.current != modePicker {
		t.Fatalf("the picker should be open")
	}
	closed := press(t, model, "esc")
	if closed.current != modeChat {
		t.Errorf("mode = %d, want chat", closed.current)
	}
}

// TestPickerMovesAndFilters covers the navigation keys, including the wrap
// around both ends and the empty-filter case.
func TestPickerMovesAndFilters(t *testing.T) {
	model := chatModel(t)
	model.openPicker("Models", "model", setupModelItems("ollama"))
	count := len(model.picker.visible)
	if count < 2 {
		t.Fatalf("the fixture needs at least two rows, got %d", count)
	}

	// Up from the first row wraps to the last.
	wrapped := press(t, model, "up")
	if wrapped.picker.cursor != count-1 {
		t.Errorf("cursor = %d, want the last row %d", wrapped.picker.cursor, count-1)
	}
	// Down from the last row wraps to the first.
	back := press(t, wrapped, "down")
	if back.picker.cursor != 0 {
		t.Errorf("cursor = %d, want the first row", back.picker.cursor)
	}

	// Backspace with nothing typed is a no-op rather than a panic.
	nothing := press(t, back, "backspace")
	if nothing.picker.filter != "" {
		t.Errorf("filter = %q, want empty", nothing.picker.filter)
	}

	typed := press(t, nothing, "qwen")
	if len(typed.picker.visible) != 1 {
		t.Fatalf("filter matched %d rows, want 1", len(typed.picker.visible))
	}
	erased := press(t, typed, "backspace")
	if erased.picker.filter != "qwe" {
		t.Errorf("filter = %q, want qwe", erased.picker.filter)
	}

	// A filter that matches nothing must render and then refuse to select.
	none := press(t, typed, "zzzz")
	if len(none.picker.visible) != 0 {
		t.Fatalf("expected no matches, got %d", len(none.picker.visible))
	}
	if !strings.Contains(display(none), "No matches") {
		t.Errorf("an empty list should say so:\n%s", display(none))
	}
	unchanged, _ := send(t, none, tea.KeyMsg{Type: tea.KeyEnter})
	if unchanged.current != modePicker {
		t.Errorf("Enter with no selection must leave the picker open")
	}
}
