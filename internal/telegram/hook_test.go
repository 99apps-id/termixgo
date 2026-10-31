package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
)

func TestTelegramNativeHookRepliesDirectlyWithoutLLM(t *testing.T) {
	agent := &scriptedAgent{answer: "llm response"}
	bot, api := pairedBot(t, agent)

	bot.SetInterceptor(func(ctx context.Context, payload channel.HookPayload) (*channel.HookResponse, error) {
		if payload.Text == "faq" {
			return &channel.HookResponse{
				Action: channel.ActionReply,
				Reply:  "Jawaban FAQ dari Native Hook",
			}, nil
		}
		return &channel.HookResponse{Action: channel.ActionContinue}, nil
	})

	bot.handleMessage(context.Background(), message("faq"))

	if agent.promptCount() != 0 {
		t.Fatalf("agent should not be called when hook replies, got count: %d", agent.promptCount())
	}
	text := lastText(t, api, "sendMessage")
	if !strings.Contains(text, "Jawaban FAQ dari Native Hook") {
		t.Fatalf("expected hook reply text, got: %q", text)
	}
}

func TestTelegramNativeHookModifiesPromptForLLM(t *testing.T) {
	agent := &scriptedAgent{answer: "ok"}
	bot, _ := pairedBot(t, agent)

	bot.SetInterceptor(func(ctx context.Context, payload channel.HookPayload) (*channel.HookResponse, error) {
		return &channel.HookResponse{
			Action: channel.ActionModify,
			Prompt: "Prefix: " + payload.Prompt,
		}, nil
	})

	bot.handleMessage(context.Background(), message("buatkan kode"))

	if agent.promptCount() != 1 {
		t.Fatalf("expected 1 prompt, got: %d", agent.promptCount())
	}
	if !strings.Contains(agent.firstPrompt(), "Prefix: buatkan kode") {
		t.Errorf("expected modified prompt, got: %q", agent.firstPrompt())
	}
}

func TestTelegramHTTPHookEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(channel.HookResponse{
			Action: channel.ActionReply,
			Reply:  "Balasan HTTP Hook Telegram",
		})
	}))
	defer srv.Close()

	agent := &scriptedAgent{answer: "llm"}
	bot, api := pairedBot(t, agent)

	bot.SetAccount("default", config.ChannelAccount{
		Enabled: true,
		HookURL: srv.URL,
	}, nil)

	bot.handleMessage(context.Background(), message("halo bot"))

	if agent.promptCount() != 0 {
		t.Fatalf("agent should not be called, got count: %d", agent.promptCount())
	}
	text := lastText(t, api, "sendMessage")
	if !strings.Contains(text, "Balasan HTTP Hook Telegram") {
		t.Fatalf("expected hook reply, got: %q", text)
	}
}
