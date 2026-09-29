package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// searchServer points both search endpoints at one test server so a run never
// reaches the network. The HTML page is tried first, so the payload is the
// instant-answer JSON an HTML scrape cannot read; every test therefore also
// exercises the fallback path.
func searchServer(t *testing.T, payload string, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("q") == "" {
			t.Errorf("the query parameter is missing from %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		fmt.Fprint(writer, payload)
	}))
	t.Cleanup(func() {
		server.Close()
		webSearchEndpoint = "https://api.duckduckgo.com/"
		webSearchHTMLEndpoint = "https://html.duckduckgo.com/html/"
	})
	webSearchEndpoint = server.URL + "/"
	webSearchHTMLEndpoint = server.URL + "/"
	return server
}

// htmlSearchServer serves one fixed markup for the HTML endpoint and an empty
// JSON body for the fallback, so a parse result can only come from the HTML.
func htmlSearchServer(t *testing.T, markup string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/" {
			writer.Header().Set("Content-Type", "text/html")
			fmt.Fprint(writer, markup)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"AbstractText":"","RelatedTopics":[]}`)
	}))
	t.Cleanup(func() {
		server.Close()
		webSearchEndpoint = "https://api.duckduckgo.com/"
		webSearchHTMLEndpoint = "https://html.duckduckgo.com/html/"
	})
	webSearchHTMLEndpoint = server.URL + "/"
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

// TestWebSearchParsesDuckDuckGoHTML covers the real backend: the result page
// markup becomes title, URL and snippet, and the redirect wrapper is unwrapped.
func TestWebSearchParsesDuckDuckGoHTML(t *testing.T) {
	htmlSearchServer(t, `<html><body>
		<div class="result results_links results_links_deep web-result">
			<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2Fgo1.26&amp;rut=abc">Go 1.26 <b>release</b> notes</a>
			<a class="result__snippet" href="x">The latest Go release adds generics to the standard library.</a>
		</div>
		<div class="result results_links results_links_deep web-result">
			<a rel="nofollow" class="result__a" href="https://pkg.go.dev/">Package index</a>
			<a class="result__snippet" href="y">Browse the standard library.</a>
		</div>
	</body></html>`)

	tool := &webSearchTool{}
	result, err := tool.Run(context.Background(), &Env{}, map[string]any{"query": "go 1.26"})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	for _, want := range []string{
		"Go 1.26 release notes",
		"https://go.dev/doc/go1.26",
		"The latest Go release adds generics to the standard library.",
		"https://pkg.go.dev/",
	} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output is missing %q:\n%s", want, result.Output)
		}
	}
	if strings.Contains(result.Output, "uddg=") || strings.Contains(result.Output, "//duckduckgo.com/l/") {
		t.Errorf("the redirect wrapper should be unwrapped:\n%s", result.Output)
	}
}

// TestWebSearchOfflineHintTellsTheModelToStop covers the failure the operator
// hit: a DNS failure must be named as offline so the agent stops retrying.
func TestWebSearchOfflineHintTellsTheModelToStop(t *testing.T) {
	result := searchFailure("go 1.26", fmt.Errorf("dial tcp: lookup html.duckduckgo.com: no such host"))
	if !result.IsError {
		t.Fatalf("a DNS failure must be an error result")
	}
	if !strings.Contains(result.Output, "offline") || !strings.Contains(result.Output, "Do not retry") {
		t.Errorf("output = %q, want the offline hint", result.Output)
	}
	if !isNetworkUnreachable(fmt.Errorf("dial tcp: lookup x: no such host")) {
		t.Errorf("no such host must read as unreachable")
	}
	if isNetworkUnreachable(fmt.Errorf("search returned 502 Bad Gateway")) {
		t.Errorf("an HTTP status is not a network failure")
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
