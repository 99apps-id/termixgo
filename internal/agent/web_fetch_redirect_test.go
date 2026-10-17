package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWebFetchRefusesARedirectToALinkLocalAddress covers the hole the direct
// host check leaves open.
//
// Run checks the URL the model asked for, but the HTTP client followed
// redirects on its own. A page on an ordinary host could therefore redirect to
// the cloud metadata address the guard exists to keep out, and the fetch
// succeeded because only the first URL was ever inspected.
func TestWebFetchRefusesARedirectToALinkLocalAddress(t *testing.T) {
	// The test server listens on loopback, which the SSRF default refuses;
	// the opt-in below plays the operator allowing their own machine, so the
	// test exercises the redirect target rather than the server address.
	t.Setenv("TERMIXGO_ALLOW_PRIVATE_FETCH", "1")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "http://[fe80::1]/latest/meta-data/", http.StatusFound)
	}))
	defer server.Close()

	tool := &webFetchTool{}
	result, err := tool.Run(context.Background(), testEnv(t), map[string]any{"url": server.URL})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsError {
		t.Fatalf("a redirect to a link-local address must be refused, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "link-local or cloud metadata") {
		t.Errorf("the refusal should name the reason, got %q", result.Output)
	}
}

// TestWebFetchStillFollowsAnAllowedRedirect keeps the guard from becoming a
// blanket that breaks ordinary pages, which redirect all the time.
func TestWebFetchStillFollowsAnAllowedRedirect(t *testing.T) {
	t.Setenv("TERMIXGO_ALLOW_PRIVATE_FETCH", "1")
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("moved here"))
	}))
	defer target.Close()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer server.Close()

	tool := &webFetchTool{}
	result, err := tool.Run(context.Background(), testEnv(t), map[string]any{"url": server.URL})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("an ordinary redirect should still be followed, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "moved here") {
		t.Errorf("the redirect target body should be returned, got %q", result.Output)
	}
}
