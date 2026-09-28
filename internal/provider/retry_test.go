package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWindowUsesTheModelThenTheProviderDefault(t *testing.T) {
	// A listed model wins over its provider default.
	gpt41 := Model{ID: "gpt-4.1", Provider: "openai"}
	if got := gpt41.Window(); got != 1047576 {
		t.Errorf("gpt-4.1 window = %d, want 1047576", got)
	}
	// An unlisted model falls back to the provider.
	unlisted := Model{ID: "some-new-model", Provider: "anthropic"}
	if got := unlisted.Window(); got != 200000 {
		t.Errorf("anthropic fallback window = %d, want 200000", got)
	}
	// A local server gets a small window, which is the case the old single
	// default got badly wrong.
	local := Model{ID: "llama3.2:latest", Provider: "ollama"}
	if got := local.Window(); got != 32768 {
		t.Errorf("ollama window = %d, want 32768", got)
	}
	// An unknown provider still gets a usable answer.
	unknown := Model{ID: "x", Provider: "nope"}
	if got := unknown.Window(); got != DefaultContextWindow {
		t.Errorf("unknown provider window = %d, want %d", got, DefaultContextWindow)
	}
}

func TestPostRetriesRateLimitThenSucceeds(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if atomic.AddInt32(&attempts, 1) < 3 {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"}}]}\n\n")
	}))
	defer server.Close()

	client, _ := newHTTPClient(Provider{ID: "openai", Label: "OpenAI", Kind: KindOpenAI}, server.URL, "key")
	events := collect(t, client, ChatRequest{Model: "m"})

	if got := joinText(events, EventTextDelta); got != "recovered" {
		t.Errorf("text = %q, want recovered", got)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestPostDoesNotRetryAnAuthFailure(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&attempts, 1)
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer server.Close()

	client, _ := newHTTPClient(Provider{ID: "openai", Label: "OpenAI", Kind: KindOpenAI}, server.URL, "bad")
	err := client.Stream(context.Background(), ChatRequest{Model: "m"}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatalf("expected an error")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("a 401 must not be retried, got %d attempts", got)
	}
	if !strings.Contains(err.Error(), "check the API key") {
		t.Errorf("the hint should point at the key, got %v", err)
	}
}

func TestPostGivesUpAfterTheAttemptsAreSpent(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&attempts, 1)
		writer.Header().Set("Retry-After", "0")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"error":{"message":"down"}}`))
	}))
	defer server.Close()

	client, _ := newHTTPClient(Provider{ID: "openai", Label: "OpenAI", Kind: KindOpenAI}, server.URL, "key")
	err := client.Stream(context.Background(), ChatRequest{Model: "m"}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatalf("expected an error after the attempts are spent")
	}
	if got := atomic.LoadInt32(&attempts); got != maxPostAttempts {
		t.Errorf("attempts = %d, want %d", got, maxPostAttempts)
	}
	if !strings.Contains(err.Error(), "down") {
		t.Errorf("the provider message should survive, got %v", err)
	}
}

func TestRetryDelayHonoursTheProviderHint(t *testing.T) {
	hinted := retryDelay(2, &retryError{err: fmt.Errorf("rate limited"), after: 3 * time.Second})
	if hinted != 3*time.Second {
		t.Errorf("delay = %s, want the 3s hint", hinted)
	}
	capped := retryDelay(2, &retryError{err: fmt.Errorf("rate limited"), after: time.Hour})
	if capped != maxRetryDelay {
		t.Errorf("delay = %s, want it capped at %s", capped, maxRetryDelay)
	}
	// An un-hinted retry still backs off, and never sleeps forever.
	plain := retryDelay(3, fmt.Errorf("boom"))
	if plain <= 0 || plain > maxRetryDelay {
		t.Errorf("delay = %s, want a positive value within the cap", plain)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("5"); got != 5*time.Second {
		t.Errorf("seconds form = %s, want 5s", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("empty = %s, want 0", got)
	}
	if got := parseRetryAfter("not-a-value"); got != 0 {
		t.Errorf("garbage = %s, want 0", got)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Errorf("a past date = %s, want 0", got)
	}
}

func TestRetryableStatus(t *testing.T) {
	retryable := []int{408, 429, 500, 502, 503, 504}
	for _, status := range retryable {
		if !retryableStatus(status) {
			t.Errorf("%d should be retryable", status)
		}
	}
	for _, status := range []int{200, 400, 401, 403, 404, 422} {
		if retryableStatus(status) {
			t.Errorf("%d must not be retried", status)
		}
	}
}

func TestSleepContextStopsEarly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := sleepContext(ctx, 5*time.Second); err == nil {
		t.Fatalf("a cancelled context should end the wait with an error")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("the wait should end immediately, took %s", elapsed)
	}
}

func TestStallGuardAbortsASilentStream(t *testing.T) {
	// The server sends one chunk then holds the connection open.
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		writer.(http.Flusher).Flush()
		<-release
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	client, _ := newHTTPClient(Provider{ID: "openai", Label: "OpenAI", Kind: KindOpenAI}, server.URL, "key")
	httpClient := client.(*openAIClient)

	// A short stall window keeps the test fast while exercising the same path.
	shortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := httpClient.postWithStall(shortCtx, httpClient.baseURL+"/chat/completions", nil, map[string]any{"model": "m"}, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()

	reader := newSSEReader(response.Body)
	if payload, err := reader.next(); err != nil || !strings.Contains(payload, "first") {
		t.Fatalf("the first chunk should arrive, got %q err=%v", payload, err)
	}
	_, err = reader.next()
	if err == nil {
		t.Fatalf("a stalled stream must fail instead of blocking forever")
	}
	if message, stalled := StallError(err, 300*time.Millisecond); !stalled {
		t.Errorf("the error should be reported as a stall, got %v", err)
	} else if !strings.Contains(message, "stopped sending data") {
		t.Errorf("the stall message should explain itself, got %q", message)
	}
}

func TestStallGuardCloseReleasesTheRequest(t *testing.T) {
	body := &countingBody{}
	stopCalled := false
	guard := newStallGuard(body, time.Hour, func() { stopCalled = true })
	if err := guard.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !stopCalled {
		t.Errorf("Close must cancel the request context")
	}
	if !body.closed {
		t.Errorf("Close must close the underlying body")
	}
}

// countingBody is a minimal ReadCloser that records Close.
type countingBody struct{ closed bool }

func (b *countingBody) Read([]byte) (int, error) { return 0, nil }
func (b *countingBody) Close() error             { b.closed = true; return nil }

func TestDefaultBaseURLIsOneSourceOfTruth(t *testing.T) {
	if got := DefaultBaseURL("openai-compatible"); got != "https://api.openai.com/v1" {
		t.Errorf("openai-compatible default = %q", got)
	}
	if got := DefaultBaseURL("ollama"); got != "http://localhost:11434/v1" {
		t.Errorf("ollama default = %q", got)
	}
	if got := DefaultBaseURL("not-a-provider"); got != "" {
		t.Errorf("an unknown provider has no default, got %q", got)
	}
}
