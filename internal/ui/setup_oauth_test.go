package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// TestWizardSendsAnOAuthProviderToLogin pins the fix: choosing a login provider
// in onboarding must not ask for an API key.
func TestWizardSendsAnOAuthProviderToLogin(t *testing.T) {
	model := chatModel(t)
	model.startSetup()

	cursor := -1
	for index, item := range model.picker.visible {
		if item.ID == "xai-oauth" {
			cursor = index
		}
	}
	if cursor < 0 {
		t.Fatalf("xai-oauth is missing from the provider list")
	}
	model.picker.cursor = cursor

	selected := press(t, model, "enter")
	if selected.setup.step != setupOAuth {
		t.Fatalf("step = %d, want setupOAuth", selected.setup.step)
	}
	if selected.current != modeSetup {
		t.Errorf("mode = %d, want modeSetup", selected.current)
	}

	// With no login yet, Enter must point at the login command rather than
	// silently continuing.
	confirmed := press(t, selected, "enter")
	if !strings.Contains(confirmed.setup.errText, "termixgo login xai-oauth") {
		t.Errorf("errText = %q, want the login instruction", confirmed.setup.errText)
	}
}

// TestProviderItemsMarkOAuthAsLogin keeps the picker honest: an OAuth provider
// reads "login required", not "API key required".
func TestProviderItemsMarkOAuthAsLogin(t *testing.T) {
	if !provider.UsesOAuth("xai-oauth") || !provider.UsesOAuth("openai-codex") {
		t.Fatalf("the OAuth providers should report UsesOAuth")
	}
	if provider.UsesOAuth("xai") {
		t.Errorf("the API-key xai provider is not OAuth")
	}
	for _, item := range setupProviderItems() {
		if item.ID == "xai-oauth" && item.Extra != "login required" {
			t.Errorf("xai-oauth extra = %q, want login required", item.Extra)
		}
	}
}
