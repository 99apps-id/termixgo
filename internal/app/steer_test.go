package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// TestSteerIsQueuedAndTaken pins the queue mechanics, including that blank
// input is refused rather than queued as an empty turn.
func TestSteerIsQueuedAndTaken(t *testing.T) {
	application := newTestApp(t)

	if got := application.SteerCount(); got != 0 {
		t.Fatalf("a fresh app has nothing queued, got %d", got)
	}
	application.Steer("   ")
	if got := application.SteerCount(); got != 0 {
		t.Errorf("a blank steer must not be queued, got %d", got)
	}

	application.Steer("also check the tests")
	if got := application.SteerCount(); got != 1 {
		t.Fatalf("SteerCount = %d, want 1", got)
	}

	taken := application.TakeSteer()
	if len(taken) != 1 || taken[0] != "also check the tests" {
		t.Errorf("TakeSteer = %q, want the queued message", taken)
	}
	// Taking is destructive, so the same message can never run twice.
	if got := application.SteerCount(); got != 0 {
		t.Errorf("SteerCount after TakeSteer = %d, want 0", got)
	}
	if again := application.TakeSteer(); again != nil {
		t.Errorf("a second TakeSteer = %q, want nothing", again)
	}
}

// TestSteerReachesTheRunningTurn is the end-to-end steer contract: a message
// typed while a turn is in flight changes that turn at its next step, instead
// of waiting for the turn to end.
func TestSteerReachesTheRunningTurn(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	stepStarted := make(chan struct{})
	release := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		step := len(bodies)
		mu.Unlock()

		writer.Header().Set("Content-Type", "text/event-stream")
		if step == 1 {
			// Hold the first step open so the test can steer while the turn is
			// genuinely in flight.
			close(stepStarted)
			<-release
			fmt.Fprintf(writer, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"think","arguments":"{\"thoughts\":\"first angle\"}"}}]}}]}`)
			fmt.Fprintf(writer, "data: %s\n\n", `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
			fmt.Fprint(writer, "data: [DONE]\n\n")
			return
		}
		fmt.Fprintf(writer, "data: %s\n\n", `{"choices":[{"delta":{"content":"steered"}}]}`)
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

	var noticeMu sync.Mutex
	var steering string
	claim := application.SetObserver(func(event agent.Event) {
		if event.Kind == agent.EventNotice && strings.HasPrefix(event.Text, "Steering:") {
			noticeMu.Lock()
			steering = event.Text
			noticeMu.Unlock()
		}
	})
	if claim == 0 {
		t.Fatalf("the observer slot should be free in this test")
	}
	defer application.ClearObserver(claim)

	done := make(chan error, 1)
	go func() { done <- application.RunTurn(context.Background(), "look at the parser") }()

	<-stepStarted
	application.Steer("also check the tests")
	close(release)

	if err := <-done; err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	mu.Lock()
	requests := append([]string{}, bodies...)
	mu.Unlock()
	if len(requests) < 2 {
		t.Fatalf("the turn made %d provider calls, want at least 2", len(requests))
	}

	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(requests[1]), &payload); err != nil {
		t.Fatalf("the second request is not the expected JSON: %v", err)
	}
	found := false
	for _, message := range payload.Messages {
		if strings.Contains(message.Content, "also check the tests") {
			found = true
		}
	}
	if !found {
		t.Errorf("the steer never reached the model: %s", requests[1])
	}

	noticeMu.Lock()
	defer noticeMu.Unlock()
	if !strings.Contains(steering, "also check the tests") {
		t.Errorf("the operator should see the steer land, got %q", steering)
	}
}

// TestSteerLeftOverAfterATurnIsReturned keeps a message typed too late to
// change anything from being silently dropped: the caller gets it back and runs
// it as the next turn.
func TestSteerLeftOverAfterATurnIsReturned(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()

	if err := application.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	// The turn is over, so nothing can take this one.
	application.Steer("a follow-up typed too late")

	left := application.TakeSteer()
	if len(left) != 1 || left[0] != "a follow-up typed too late" {
		t.Errorf("TakeSteer = %q, want the late steer back", left)
	}
}
