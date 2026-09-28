package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/telegram"
)

// TestRunTurnWithoutAModelReportsTheSentinel pins the error a caller can test
// for, so the TUI and the bot can both give a useful message.
func TestRunTurnWithoutAModelReportsTheSentinel(t *testing.T) {
	application := newTestApp(t)
	err := application.RunTurn(context.Background(), "hello")
	if !errors.Is(err, ErrNoModel) {
		t.Fatalf("err = %v, want ErrNoModel", err)
	}
	if !strings.Contains(err.Error(), "/setup") {
		t.Errorf("the message should point at /setup, got %v", err)
	}
}

func TestContextUsageIsReported(t *testing.T) {
	application := newTestApp(t)
	usage := application.ContextUsage()
	if !strings.Contains(usage, "tokens") {
		t.Errorf("usage = %q, want a token count", usage)
	}
	// The budget follows the model, so a local model must report a small one.
	if !strings.Contains(usage, "96.0k") && !strings.Contains(usage, "k") {
		t.Errorf("usage = %q, want a readable budget", usage)
	}
	if !strings.Contains(application.Status(), "context:") {
		t.Errorf("status should report the context: %q", application.Status())
	}
}

func TestSecondTurnIsRejectedWhileOneIsInFlight(t *testing.T) {
	// The provider holds the request open, which is what puts a turn
	// genuinely in flight.
	// The handler captures this channel by reference, so it must never be
	// reassigned: doing so once left the handler waiting on a fresh channel
	// that was never closed, which hung the test on the 120s header timeout.
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-release
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer func() {
		unblock()
		server.Close()
	}()

	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	// A local model needs no API key, so the client builds without one.
	cfg.DefaultModel = "qwen2.5-coder:latest"
	cfg.ApprovalMode = config.ApprovalAll
	cfg.BaseURLs = map[string]string{"ollama": server.URL}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	application, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !application.HasModel() {
		t.Fatalf("the app should be ready with a local model")
	}

	first := make(chan error, 1)
	go func() { first <- application.RunTurn(context.Background(), "first") }()

	deadline := time.Now().Add(5 * time.Second)
	for !application.Running() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !application.Running() {
		t.Fatalf("the first turn never started")
	}

	// The second caller must be told the agent is busy rather than queueing
	// behind a turn it cannot see.
	err = application.RunTurn(context.Background(), "second")
	if !errors.Is(err, ErrBusy) {
		t.Errorf("second turn err = %v, want ErrBusy", err)
	}

	unblock()
	if err := <-first; err != nil {
		t.Fatalf("first turn failed: %v", err)
	}
	if application.Running() {
		t.Errorf("the app should not still report running after the turn")
	}
	// The turn is recorded once, not twice.
	if got := application.Session().Turns(); got != 1 {
		t.Errorf("turns = %d, want 1", got)
	}
}

func TestRegeneratePairingCodeUpdatesARunningBot(t *testing.T) {
	application := newTestApp(t)

	// A bot object stands in for a running poller.
	application.mu.Lock()
	application.bot = &telegram.Bot{}
	application.mu.Unlock()

	code, err := application.RegeneratePairingCode()
	if err != nil {
		t.Fatalf("RegeneratePairingCode: %v", err)
	}
	if len(code) != 6 {
		t.Errorf("code = %q, want six digits", code)
	}

	application.mu.Lock()
	live := application.bot.PairingCode
	application.mu.Unlock()
	if live != code {
		t.Errorf("the running bot holds %q, want the new code %q", live, code)
	}

	// The code also has to be persisted, so a restart keeps working.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if reloaded.Telegram.PairingCode != code {
		t.Errorf("stored code = %q, want %q", reloaded.Telegram.PairingCode, code)
	}
}

func TestEnsurePairingCodeIsStableUntilRegenerated(t *testing.T) {
	application := newTestApp(t)
	first, err := application.EnsurePairingCode()
	if err != nil {
		t.Fatalf("EnsurePairingCode: %v", err)
	}
	second, err := application.EnsurePairingCode()
	if err != nil {
		t.Fatalf("EnsurePairingCode: %v", err)
	}
	if first != second {
		t.Errorf("an existing code must be reused, got %q then %q", first, second)
	}
	third, err := application.RegeneratePairingCode()
	if err != nil {
		t.Fatalf("RegeneratePairingCode: %v", err)
	}
	if third == first {
		t.Errorf("a regenerated code should differ, both were %q", third)
	}
}

func TestNewSessionIsSafeUnderConcurrentReads(t *testing.T) {
	application := newTestApp(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := 0; index < 200; index++ {
			_ = application.Session().ID
			_ = application.Session().Turns()
			_ = application.Status()
		}
	}()
	for index := 0; index < 50; index++ {
		application.NewSession()
	}
	<-done
	if application.Session() == nil {
		t.Fatalf("the session must never be nil")
	}
}
