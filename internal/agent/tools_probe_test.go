package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeURLToolSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	tool := &probeURLTool{}
	env := &Env{Trusted: true}

	res, err := tool.Run(context.Background(), env, map[string]any{
		"url":             ts.URL,
		"expected_status": 200,
	})
	if err != nil {
		t.Fatalf("probe failed with error: %v", err)
	}
	if res.IsError {
		t.Fatalf("probe returned error result: %s", res.Output)
	}
	if !strings.Contains(res.Output, "HTTP 200 OK") {
		t.Errorf("expected 200 OK, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, `{"status":"ok"}`) {
		t.Errorf("expected body in output, got: %s", res.Output)
	}
}

func TestProbeURLToolStatusMismatch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`not found`))
	}))
	defer ts.Close()

	tool := &probeURLTool{}
	env := &Env{Trusted: true}

	res, err := tool.Run(context.Background(), env, map[string]any{
		"url":             ts.URL,
		"expected_status": 200,
	})
	if err != nil {
		t.Fatalf("probe failed with error: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError=true on status mismatch, got false")
	}
	if !strings.Contains(res.Output, "did not match expected 200") {
		t.Errorf("expected warning note in output: %s", res.Output)
	}
}

func TestProbeURLToolRejectsNonLoopback(t *testing.T) {
	tool := &probeURLTool{}
	env := &Env{Trusted: true}

	for _, badURL := range []string{
		"http://example.com/health",
		"http://192.168.1.1/api",
		"http://8.8.8.8/",
	} {
		res, err := tool.Run(context.Background(), env, map[string]any{
			"url": badURL,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.IsError {
			t.Errorf("expected rejection for non-loopback URL %s, got: %s", badURL, res.Output)
		}
		if !strings.Contains(res.Output, "restricted to loopback addresses") {
			t.Errorf("expected loopback restriction message, got: %s", res.Output)
		}
	}
}

func TestProbeURLToolRejectsUnsupportedScheme(t *testing.T) {
	tool := &probeURLTool{}
	env := &Env{Trusted: true}

	res, err := tool.Run(context.Background(), env, map[string]any{
		"url": "file:///etc/passwd",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected rejection for non-http scheme, got: %s", res.Output)
	}
}

func TestProbeURLToolRedirectProtection(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://external-evil.com/leak", http.StatusFound)
	}))
	defer ts.Close()

	tool := &probeURLTool{}
	env := &Env{Trusted: true}

	res, err := tool.Run(context.Background(), env, map[string]any{
		"url": ts.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected error when redirected to external host, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "redirect to non-loopback host") {
		t.Errorf("expected redirect blocked error, got: %s", res.Output)
	}
}
