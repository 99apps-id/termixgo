package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

// readyApp returns an app configured against a local test provider, plus the
// server it talks to. The provider is a local one so no API key is needed,
// which keeps the test focused on the behaviour under test.
//
// Callers must close the returned server.
func readyApp(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))

	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	cfg.DefaultModel = "qwen2.5-coder:latest"
	cfg.ApprovalMode = config.ApprovalAll
	cfg.BaseURLs = map[string]string{"ollama": server.URL}
	if err := config.Save(cfg); err != nil {
		server.Close()
		t.Fatalf("save config: %v", err)
	}

	application, err := newApp(t, t.TempDir())
	if err != nil {
		server.Close()
		t.Fatalf("New: %v", err)
	}
	if !application.HasModel() {
		server.Close()
		t.Fatalf("the app should be ready with a local model")
	}
	return application, server
}

// readyAppHeld is readyApp with a provider that does not answer until release
// is closed. That is what puts a turn genuinely in flight, which tests about
// cancellation and the single-run slot need.
//
// Callers must close release and the returned server.
func readyAppHeld(t *testing.T, release <-chan struct{}) (*App, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-release
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"late\"}}]}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))

	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	cfg.DefaultModel = "qwen2.5-coder:latest"
	cfg.ApprovalMode = config.ApprovalAll
	cfg.BaseURLs = map[string]string{"ollama": server.URL}
	if err := config.Save(cfg); err != nil {
		server.Close()
		t.Fatalf("save config: %v", err)
	}

	application, err := newApp(t, t.TempDir())
	if err != nil {
		server.Close()
		t.Fatalf("New: %v", err)
	}
	return application, server
}
