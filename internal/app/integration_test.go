package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// TestRunTurnEndToEnd drives the whole stack: config, secret store, provider
// client, HTTP streaming, the run loop and the session, with no terminal.
func TestRunTurnEndToEnd(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "greeting.txt"), []byte("hello from the workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		var payload struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(request.Body).Decode(&payload)
		// The second call already has a tool result, so answer with prose.
		hasToolResult := false
		for _, message := range payload.Messages {
			if message.Role == "tool" {
				hasToolResult = true
			}
		}
		var chunks []string
		if hasToolResult {
			chunks = []string{
				`{"choices":[{"delta":{"content":"The greeting file says hello."}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			}
		} else {
			chunks = []string{
				`{"choices":[{"delta":{"reasoning_content":"Reading the file first."}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"path\":\"greeting.txt\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			}
		}
		for _, chunk := range chunks {
			fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	cfg.DefaultModel = "gpt-5.4-mini"
	cfg.ApprovalMode = config.ApprovalAll
	cfg.BaseURLs = map[string]string{"openai": server.URL}
	cfg = cfg.Trust(directory)
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	store, err := secrets.Load()
	if err != nil {
		t.Fatalf("load secrets: %v", err)
	}
	if err := store.Set(secrets.ProviderKey("openai"), "sk-test"); err != nil {
		t.Fatalf("store key: %v", err)
	}

	application, err := New(directory)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if application.NeedsSetup() {
		t.Fatalf("the app should be ready with a configured model and key")
	}

	if err := application.RunTurn(context.Background(), "what does greeting.txt say?"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	// Drain the events the run produced; the channel is buffered, so the run
	// never blocks on a slow consumer.
	var kinds []agent.EventKind
drain:
	for {
		select {
		case event := <-application.Events():
			kinds = append(kinds, event.Kind)
		default:
			break drain
		}
	}

	session := application.Session()
	if session.Turns() != 1 {
		t.Errorf("turns = %d, want 1", session.Turns())
	}
	var toolOutput, finalAnswer string
	for _, message := range session.Messages() {
		switch message.Role {
		case provider.RoleTool:
			toolOutput = message.Content
		case provider.RoleAssistant:
			if message.Content != "" {
				finalAnswer = message.Content
			}
		}
	}
	if !strings.Contains(toolOutput, "hello from the workspace") {
		t.Errorf("the tool output was not captured: %q", toolOutput)
	}
	if !strings.Contains(finalAnswer, "hello") {
		t.Errorf("the final answer was not captured: %q", finalAnswer)
	}

	found := map[agent.EventKind]bool{}
	for _, kind := range kinds {
		found[kind] = true
	}
	for _, want := range []agent.EventKind{agent.EventThinking, agent.EventReasoned, agent.EventToolStart, agent.EventToolEnd, agent.EventText} {
		if !found[want] {
			t.Errorf("missing %s event", want)
		}
	}
}

// TestRunTurnWithoutModelExplainsSetup checks the friendly failure path.
func TestRunTurnWithoutModelExplainsSetup(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	application, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = application.RunTurn(context.Background(), "hello")
	if err == nil {
		t.Fatalf("a run without a model must fail")
	}
	if !strings.Contains(err.Error(), "/setup") {
		t.Errorf("the error should point at /setup, got %v", err)
	}
}

func TestStatusThroughTheStack(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	application, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	status := application.Status()
	if !strings.Contains(status, "approval: all") {
		t.Errorf("status should report the approval mode: %q", status)
	}
	if !strings.Contains(status, "session:") {
		t.Errorf("status should report the session: %q", status)
	}
}
