package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTLSInspectLocalServer(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tool := &tlsInspectTool{}

	// Blocked host rejection
	res, err := tool.Run(context.Background(), nil, map[string]any{"host": "169.254.169.254"})
	if err != nil || !res.IsError || !strings.Contains(res.Output, "blocked") {
		t.Fatalf("expected blocked host error, got %v, out: %s", err, res.Output)
	}

	// Empty host
	res, _ = tool.Run(context.Background(), nil, map[string]any{"host": ""})
	if !res.IsError {
		t.Fatalf("expected error for empty host")
	}

	// Inspect the test TLS server
	res, err = tool.Run(context.Background(), nil, map[string]any{
		"host": ts.URL,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(res.Output, "TLS Inspection Report") {
		t.Fatalf("expected report header in output, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "Primary Certificate") {
		t.Fatalf("expected cert details in output, got: %s", res.Output)
	}
}

func TestHTTPHeadersAudit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		w.Header().Set("Content-Security-Policy", "default-src 'self'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Server", "Apache/2.4.50")
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    "secret123",
			HttpOnly: true,
			Secure:   false,
		})
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tool := &httpHeadersAuditTool{}

	// Blocked host check
	res, _ := tool.Run(context.Background(), nil, map[string]any{"url": "http://169.254.169.254"})
	if !res.IsError {
		t.Fatalf("expected error for blocked host")
	}

	// Empty url
	res, _ = tool.Run(context.Background(), nil, map[string]any{"url": ""})
	if !res.IsError {
		t.Fatalf("expected error for empty url")
	}

	// Audit headers on test server
	res, err := tool.Run(context.Background(), nil, map[string]any{
		"url": ts.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Output, "HTTP Security Headers Audit") {
		t.Fatalf("expected audit header in output, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "Strict-Transport-Security") || !strings.Contains(res.Output, "X-Frame-Options") {
		t.Fatalf("expected audited security headers, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "Apache/2.4.50") {
		t.Fatalf("expected detected server disclosure header, got: %s", res.Output)
	}
}

func TestDNSReconInputValidation(t *testing.T) {
	tool := &dnsReconTool{}

	res, _ := tool.Run(context.Background(), nil, map[string]any{"domain": ""})
	if !res.IsError {
		t.Fatalf("expected error for empty domain")
	}

	res, _ = tool.Run(context.Background(), nil, map[string]any{"domain": "169.254.169.254"})
	if !res.IsError || !strings.Contains(res.Output, "blocked") {
		t.Fatalf("expected blocked address error")
	}
}

func TestPortProbe(t *testing.T) {
	// Start a local TCP listener
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	tool := &portProbeTool{}

	// Blocked host check
	res, _ := tool.Run(context.Background(), nil, map[string]any{
		"host":  "169.254.169.254",
		"ports": []any{float64(port)},
	})
	if !res.IsError {
		t.Fatalf("expected blocked host error")
	}

	// Probe open port and an unused high port
	res, err = tool.Run(context.Background(), nil, map[string]any{
		"host":       "127.0.0.1",
		"ports":      []any{float64(port), float64(59999)},
		"timeout_ms": float64(500),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Output, "Port Probe Report") {
		t.Fatalf("expected report header in output, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "open") {
		t.Fatalf("expected listener port to be reported open, got: %s", res.Output)
	}
}
