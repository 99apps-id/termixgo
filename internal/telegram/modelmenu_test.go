package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// callbackData finds the first inline button whose callback data has a prefix.
func callbackData(t *testing.T, payload map[string]any, prefix string) string {
	t.Helper()
	markup, _ := payload["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	for _, row := range rows {
		buttons, _ := row.([]any)
		for _, button := range buttons {
			entry, _ := button.(map[string]any)
			if data, _ := entry["callback_data"].(string); strings.HasPrefix(data, prefix) {
				return data
			}
		}
	}
	return ""
}

func callbackMessage(data string) *CallbackQuery {
	return &CallbackQuery{
		ID:      "cb",
		From:    &User{ID: 9},
		Data:    data,
		Message: &Message{MessageID: 1, Chat: Chat{ID: 7, Type: "private"}},
	}
}

// TestModelMenuListsProvidersThenModelsThenSelects walks the whole picker: the
// /model command opens a provider keyboard, a provider press lists its models,
// and a model press switches the model.
func TestModelMenuListsProvidersThenModelsThenSelects(t *testing.T) {
	agent := &scriptedAgent{model: "old-model"}
	bot, api := pairedBot(t, agent)

	bot.handleMessage(context.Background(), message("/model"))
	sent := api.calls("sendMessage")
	if len(sent) == 0 {
		t.Fatalf("no model menu was sent")
	}
	providerData := callbackData(t, sent[len(sent)-1], modelProviderPrefix)
	if providerData == "" {
		t.Fatalf("the model menu carried no provider button: %v", sent[len(sent)-1])
	}

	bot.handleCallback(context.Background(), callbackMessage(providerData))
	edits := api.calls("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("pressing a provider did not list its models")
	}
	modelData := callbackData(t, edits[len(edits)-1], modelSelectPrefix)
	if modelData == "" {
		t.Fatalf("the provider list carried no model button: %v", edits[len(edits)-1])
	}

	bot.handleCallback(context.Background(), callbackMessage(modelData))
	want := strings.TrimPrefix(modelData, modelSelectPrefix)
	if agent.Model() != want {
		t.Errorf("model = %q, want %q", agent.Model(), want)
	}
}

// TestModelMenuIgnoresAStranger keeps the picker behind the pairing gate. The
// callback handler runs on its own goroutine, so the gate must be checked there
// rather than only in handleMessage.
func TestModelMenuIgnoresAStranger(t *testing.T) {
	agent := &scriptedAgent{model: "old-model"}
	bot, api := pairedBot(t, agent)

	query := callbackMessage(modelSelectPrefix + "gpt-6-astra")
	query.From = &User{ID: 404}
	bot.handleCallback(context.Background(), query)

	if agent.Model() != "old-model" {
		t.Errorf("a stranger switched the model to %q", agent.Model())
	}
	if api.called("editMessageText") {
		t.Errorf("a stranger must not get an edit")
	}
}

func TestModelMenuOnlyShowsActiveProviders(t *testing.T) {
	agent := &scriptedAgent{
		model: "claude-sonnet-4-5",
		activeProviders: []provider.Provider{
			{ID: "anthropic", Label: "Anthropic"},
		},
	}
	bot, api := pairedBot(t, agent)

	bot.handleMessage(context.Background(), message("/model"))
	sent := api.calls("sendMessage")
	if len(sent) == 0 {
		t.Fatalf("no model menu was sent")
	}
	last := sent[len(sent)-1]
	markup, _ := last["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row of buttons, got %d", len(rows))
	}
	buttons, _ := rows[0].([]any)
	if len(buttons) != 1 {
		t.Fatalf("expected 1 button, got %d", len(buttons))
	}
	btn, _ := buttons[0].(map[string]any)
	if btn["text"] != "Anthropic" {
		t.Errorf("expected Anthropic button, got %v", btn["text"])
	}
	if btn["callback_data"] != modelProviderPrefix+"anthropic" {
		t.Errorf("callback data = %v", btn["callback_data"])
	}
}
