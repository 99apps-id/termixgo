package app

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/telegram"
)

// A chat's turn starts from the plan the chat saved, so the model sees the
// item it left open last turn and can tick it off; the terminal's plan never
// reaches the chat. Before, every chat turn began with an empty plan.
func TestChannelTurnSeesTheChatPlan(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	cfg.DefaultModel = "qwen2.5-coder:latest"
	cfg.ApprovalMode = config.ApprovalAll
	cfg.BaseURLs = map[string]string{"ollama": server.URL}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = application.Todos().Write([]agent.Todo{{Title: "terminal work", Status: "in_progress"}})

	bridge := &telegramBridge{
		app:     application,
		name:    "default",
		account: config.TelegramAccount{ChannelAccount: config.ChannelAccount{Enabled: true}},
	}
	req := channel.Request{Channel: config.ChannelTelegram, Account: "default", ChatID: "88", Admin: true}
	session, err := bridge.session(req)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if info := application.sessionPlanInfo(session, "en"); info != "No active plan in this session." {
		t.Fatalf("the terminal plan leaked into the chat: %q", info)
	}
	session.SetTodos([]agent.Todo{{ID: "t1", Title: "chat work", Status: "in_progress"}})
	if err := session.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := bridge.RunPromptStream(t.Context(), req, "continue", func(telegram.StreamEvent) {}); err != nil {
		t.Fatalf("RunPromptStream: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 {
		t.Fatalf("no request reached the model")
	}
	first := bodies[0]
	if !strings.Contains(first, "t1: chat work") || strings.Contains(first, "terminal work") {
		t.Fatalf("the turn should carry the chat's plan only")
	}
	after, _ := bridge.session(req)
	if todos := after.Todos(); len(todos) != 1 || todos[0].Title != "chat work" {
		t.Fatalf("the chat plan should be saved back, got %+v", todos)
	}
}
