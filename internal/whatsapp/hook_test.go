package whatsapp

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

func TestWhatsAppNativeHookRepliesDirectlyWithoutLLM(t *testing.T) {
	agent := &fakeAgent{}
	var replies []string
	bot := New(nil, agent)
	bot.sendHook = func(chatID, text string) {
		replies = append(replies, text)
	}
	bot.SetAccount("default", config.ChannelAccount{
		Enabled:  true,
		Admins:   []string{"628999@s.whatsapp.net"},
		DMPolicy: "open",
	}, nil)

	bot.SetInterceptor(func(ctx context.Context, payload channel.HookPayload) (*channel.HookResponse, error) {
		if payload.Text == "menu" {
			return &channel.HookResponse{
				Action: channel.ActionReply,
				Reply:  "Menu Resto Hook: 1. Nasi Goreng 2. Mie Goreng",
			}, nil
		}
		return &channel.HookResponse{Action: channel.ActionContinue}, nil
	})

	bot.handleMessage(context.Background(), testEvent("menu"))

	if len(agent.prompts) != 0 {
		t.Fatalf("agent must not be invoked when hook replies directly, got prompts: %v", agent.prompts)
	}
	if len(replies) != 1 || !strings.Contains(replies[0], "Menu Resto Hook") {
		t.Fatalf("expected direct hook reply, got: %v", replies)
	}
}

func TestWhatsAppNativeHookModifiesPromptForLLM(t *testing.T) {
	agent := &fakeAgent{}
	var replies []string
	bot := New(nil, agent)
	bot.sendHook = func(chatID, text string) {
		replies = append(replies, text)
	}
	bot.SetAccount("default", config.ChannelAccount{
		Enabled:  true,
		Admins:   []string{"628999@s.whatsapp.net"},
		DMPolicy: "open",
	}, nil)

	bot.SetInterceptor(func(ctx context.Context, payload channel.HookPayload) (*channel.HookResponse, error) {
		return &channel.HookResponse{
			Action: channel.ActionModify,
			Prompt: "Context from CRM: User Alice\n\n" + payload.Prompt,
		}, nil
	})

	bot.handleMessage(context.Background(), testEvent("tanya order"))

	if len(agent.prompts) != 1 {
		t.Fatalf("expected 1 prompt to agent, got %d", len(agent.prompts))
	}
	if !strings.Contains(agent.prompts[0], "Context from CRM: User Alice") {
		t.Errorf("expected modified prompt with CRM context, got: %q", agent.prompts[0])
	}
}

func TestWhatsAppHTTPHookEndToEnd(t *testing.T) {
	turnCompletedCalled := make(chan bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload channel.HookPayload
		_ = json.NewDecoder(r.Body).Decode(&payload)

		if payload.Event == channel.EventTurnCompleted {
			turnCompletedCalled <- true
			w.WriteHeader(http.StatusOK)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(channel.HookResponse{
			Action: channel.ActionReply,
			Reply:  "Balasan HTTP Hook REST",
		})
	}))
	defer srv.Close()

	agent := &fakeAgent{}
	var replies []string
	bot := New(nil, agent)
	bot.sendHook = func(chatID, text string) {
		replies = append(replies, text)
	}
	bot.SetAccount("default", config.ChannelAccount{
		Enabled:  true,
		Admins:   []string{"628999@s.whatsapp.net"},
		DMPolicy: "open",
		HookURL:  srv.URL,
	}, nil)

	bot.handleMessage(context.Background(), testEvent("halo http hook"))

	if len(agent.prompts) != 0 {
		t.Fatalf("agent must not be called, got prompts: %v", agent.prompts)
	}
	if len(replies) != 1 || replies[0] != "Balasan HTTP Hook REST" {
		t.Fatalf("expected HTTP hook reply, got: %v", replies)
	}
}
