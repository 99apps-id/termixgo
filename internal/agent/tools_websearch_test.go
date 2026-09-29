package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func searchServer(t *testing.T, payload string, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("format") != "json" {
			t.Errorf("format = %q, want json", request.URL.Query().Get("format"))
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		fmt.Fprint(writer, payload)
	}))
	t.Cleanup(func() {
		server.Close()
		webSearchEndpoint = "https://api.duckduckgo.com/"
	})
	webSearchEndpoint = server.URL + "/"
	return server
}

func TestWebSearchReturnsTitlesAndURLs(t *testing.T) {
	searchServer(t, `{"AbstractText":"","RelatedTopics":[{"Text":"Go 1.26 release notes","FirstURL":"https://go.dev/doc/go1.26"},{"Text":"Tutorial","FirstURL":"https://go.dev/tour"}]}`, http.StatusOK)
	tool := &webSearchTool{}
	result, err := tool.Run(context.Background(), &Env{}, map[string]any{"query": "go 1.26"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("result is an error: %q", result.Output)
	}
	for _, want := range []string{"Go 1.26 release notes", "https://go.dev/doc/go1.26"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output is missing %q:\n%s", want, result.Output)
		}
	}
}

func TestWebSearchFlattensGroupedTopics(t *testing.T) {
	searchServer(t, `{"RelatedTopics":[{"Name":"Group","Topics":[{"Text":"Nested hit","FirstURL":"https://example.com/nested"}]}]}`, http.StatusOK)
	tool := &webSearchTool{}
	result, _ := tool.Run(context.Background(), &Env{}, map[string]any{"query": "x"})
	if !strings.Contains(result.Output, "Nested hit") {
		t.Errorf("grouped topics should be flattened:\n%s", result.Output)
	}
}

func TestWebSearchRequiresAQuery(t *testing.T) {
	tool := &webSearchTool{}
	result, _ := tool.Run(context.Background(), &Env{}, map[string]any{"query": "  "})
	if !result.IsError {
		t.Errorf("an empty query must report an error")
	}
}

func TestWebSearchReportsNoResults(t *testing.T) {
	searchServer(t, `{"RelatedTopics":[]}`, http.StatusOK)
	tool := &webSearchTool{}
	result, _ := tool.Run(context.Background(), &Env{}, map[string]any{"query": "zzz unlikely"})
	if result.IsError || !strings.Contains(result.Output, "No results") {
		t.Errorf("an empty set should say so, got %q", result.Output)
	}
}

func TestWebSearchReportsServerErrors(t *testing.T) {
	searchServer(t, `oops`, http.StatusBadGateway)
	tool := &webSearchTool{}
	result, _ := tool.Run(context.Background(), &Env{}, map[string]any{"query": "x"})
	if !result.IsError {
		t.Errorf("a bad status must report an error")
	}
}

func TestWebSearchRespectsCount(t *testing.T) {
	searchServer(t, `{"RelatedTopics":[{"Text":"one","FirstURL":"https://e.com/1"},{"Text":"two","FirstURL":"https://e.com/2"},{"Text":"three","FirstURL":"https://e.com/3"}]}`, http.StatusOK)
	tool := &webSearchTool{}
	result, _ := tool.Run(context.Background(), &Env{}, map[string]any{"query": "x", "count": 2})
	if strings.Contains(result.Output, "three") {
		t.Errorf("only two results were asked for:\n%s", result.Output)
	}
}
