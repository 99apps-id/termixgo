package ui

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/app"
	"github.com/99apps-id/termixgo/internal/config"
)

// onceApp builds an app whose local model points at a test server, so a real
// turn can run with no key and no network.
func onceApp(t *testing.T, baseURL string) *app.App {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())

	cfg := config.Default()
	cfg.DefaultModel = "qwen2.5-coder:latest"
	cfg.ApprovalMode = config.ApprovalAll
	cfg.BaseURLs = map[string]string{"ollama": baseURL}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	application, err := app.New(t.TempDir())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if !application.HasModel() {
		t.Fatalf("the fixture should have a usable model")
	}
	return application
}

// TestRunOncePrintsTheWholeAnswer is the regression for a lost-output bug: the
// event drain used to be stopped with a deferred close, which could fire
// before the printer goroutine had run once, so a one-shot run printed nothing
// at all. The answer arrives as several events, which is what makes the race
// visible.
func TestRunOncePrintsTheWholeAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{"All ", "the ", "answer ", "arrived."} {
			fmt.Fprintf(writer, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", chunk)
		}
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	application := onceApp(t, server.URL)
	var out bytes.Buffer
	if err := RunOnceWithContext(context.Background(), application, "say something", &out); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	// Run it a few times: a lost tail is a race, and one pass could hide it.
	for index := 0; index < 5; index++ {
		out.Reset()
		if err := RunOnceWithContext(context.Background(), application, "say something", &out); err != nil {
			t.Fatalf("RunOnce pass %d: %v", index, err)
		}
		if !strings.Contains(out.String(), "All the answer arrived.") {
			t.Fatalf("pass %d printed %q, want the whole answer", index, out.String())
		}
	}
}

// TestRunOnceReportsAProviderFailure pins the exit status a script depends on.
// The run loop reports a rejected request as an event and still returns nil,
// so without this `termixgo -p` exited 0 on a turn that never happened.
func TestRunOnceReportsAProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// 401 is not retryable, so the failure is immediate rather than after
		// a backoff.
		writer.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(writer, `{"error":{"message":"invalid api key","type":"auth"}}`)
	}))
	defer server.Close()

	application := onceApp(t, server.URL)
	var out bytes.Buffer
	err := RunOnceWithContext(context.Background(), application, "say something", &out)
	if err == nil {
		t.Fatalf("a rejected request must be reported to the caller, output was %q", out.String())
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("err = %v, want the provider message", err)
	}
	// The operator still sees it on the stream, because plain mode is read by
	// people as well as scripts.
	if !strings.Contains(out.String(), "error:") {
		t.Errorf("output = %q, want the error printed", out.String())
	}
}

// TestPrinterRemembersOnlyTheFirstFailure keeps the one-shot report stable: a
// retry storm must not make the caller print the same problem four times.
func TestPrinterRemembersOnlyTheFirstFailure(t *testing.T) {
	var out bytes.Buffer
	printer := newPlainPrinter(&out, false)
	printer.print(agent.Event{Kind: agent.EventError, Err: fmt.Errorf("first")})
	printer.print(agent.Event{Kind: agent.EventError, Err: fmt.Errorf("second")})
	if printer.failure == nil || printer.failure.Error() != "first" {
		t.Errorf("failure = %v, want the first one", printer.failure)
	}
	if strings.Count(out.String(), "error:") != 2 {
		t.Errorf("both failures should still be shown to the operator: %q", out.String())
	}
}
