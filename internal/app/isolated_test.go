package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

// TestRunIsolatedTurnLeavesTheLiveSessionAlone pins the contract the HTTP API
// relies on: an integration gets an answer, and the operator's conversation is
// untouched.
func TestRunIsolatedTurnLeavesTheLiveSessionAlone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"isolated answer\"}}]}\n\n")
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
	before := application.Session().MessageCount()

	answer, err := application.RunIsolatedTurn(context.Background(), "do the thing", "", 5)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if answer != "isolated answer" {
		t.Fatalf("answer = %q", answer)
	}
	if got := application.Session().MessageCount(); got != before {
		t.Fatalf("the live session changed: %d -> %d", before, got)
	}
}

func TestRunIsolatedTurnRefusesAnEmptyPrompt(t *testing.T) {
	application := newTestApp(t)
	if _, err := application.RunIsolatedTurn(context.Background(), "   ", "", 0); err == nil {
		t.Fatal("an empty prompt must be refused")
	}
}
