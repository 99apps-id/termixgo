package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// restoreSearchEndpoints puts every search source back to its production URL.
func restoreSearchEndpoints() {
	webSearchEndpoint = "https://api.duckduckgo.com/"
	webSearchHTMLEndpoint = "https://html.duckduckgo.com/html/"
	webSearchWikipediaEndpoint = "https://en.wikipedia.org/w/api.php"
	webSearchGitHubEndpoint = "https://api.github.com/search/repositories"
	webSearchTavilyEndpoint = "https://api.tavily.com/search"
	webSearchBraveEndpoint = "https://api.search.brave.com/res/v1/web/search"
}

// TestDialFallsBackToDoH is the DNS-block fix: when the system resolver cannot
// resolve a name, the dialer resolves it over HTTPS and connects to the pinned
// IP, which is what lets keyless search work behind an ISP that blocks a host.
func TestDialFallsBackToDoH(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	doh := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/dns-json")
		fmt.Fprint(writer, `{"Status":0,"Answer":[{"name":"blocked.invalid","type":1,"data":"127.0.0.1"}]}`)
	}))
	t.Cleanup(func() {
		doh.Close()
		dohEndpoint = "https://cloudflare-dns.com/dns-query"
	})
	dohEndpoint = doh.URL

	conn, err := dialWithDoHFallback(context.Background(), "tcp", fmt.Sprintf("blocked.invalid:%d", port))
	if err != nil {
		t.Fatalf("dial through DoH: %v", err)
	}
	conn.Close()
}

// TestWebSearchPrefersAConfiguredTavilyKey proves a keyed provider answers
// before the keyless chain, which is what works where DuckDuckGo is DNS-blocked.
func TestWebSearchPrefersAConfiguredTavilyKey(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "test-key")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("Tavily search must be a POST, got %s", request.Method)
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), "test-key") {
			t.Errorf("the API key is not in the request body: %s", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"results":[{"title":"Go","url":"https://go.dev","content":"The Go language"}]}`)
	}))
	t.Cleanup(func() {
		server.Close()
		restoreSearchEndpoints()
	})
	webSearchTavilyEndpoint = server.URL
	// The keyless sources are unreachable, so only Tavily can answer.
	webSearchHTMLEndpoint = "http://search.invalid/"
	webSearchEndpoint = "http://search.invalid/"
	webSearchWikipediaEndpoint = "http://search.invalid/"
	webSearchGitHubEndpoint = "http://search.invalid/"

	tool := &webSearchTool{}
	result, err := tool.Run(context.Background(), &Env{}, map[string]any{"query": "go"})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	for _, want := range []string{"tavily", "https://go.dev"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output is missing %q:\n%s", want, result.Output)
		}
	}
}

// emptySources stands in for Wikipedia and GitHub with valid but empty
// payloads, so a test that exercises DuckDuckGo never reaches the network and
// the extra sources contribute no results of their own.
func emptySources(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("action") == "opensearch" {
			fmt.Fprint(writer, `["q",[],[],[]]`)
			return
		}
		fmt.Fprint(writer, `{"items":[]}`)
	}))
	t.Cleanup(server.Close)
	webSearchWikipediaEndpoint = server.URL
	webSearchGitHubEndpoint = server.URL
}

// searchServer points both DuckDuckGo endpoints at one test server so a run
// never reaches the network. The HTML page is tried first, so the payload is
// the instant-answer JSON an HTML scrape cannot read; every test therefore
// also exercises the fallback path.
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
		restoreSearchEndpoints()
	})
	webSearchEndpoint = server.URL + "/"
	webSearchHTMLEndpoint = server.URL + "/"
	emptySources(t)
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
		restoreSearchEndpoints()
	})
	webSearchHTMLEndpoint = server.URL + "/"
	webSearchEndpoint = server.URL + "/"
	emptySources(t)
	return server
}

// TestWebSearchFallsBackToWikipedia proves the chain keeps answering when
// every DuckDuckGo host is unreachable, which is the outage the operator hit:
// both DDG endpoints fail, and the Wikipedia source still returns results.
func TestWebSearchFallsBackToWikipedia(t *testing.T) {
	ddg := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "blocked", http.StatusForbidden)
	}))
	wiki := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("action") != "opensearch" {
			t.Errorf("the opensearch action is missing: %s", request.URL.RawQuery)
		}
		fmt.Fprint(writer, `["go",["Go (programming language)"],["Statically typed language"],["https://en.wikipedia.org/wiki/Go_(programming_language)"]]`)
	}))
	t.Cleanup(func() {
		ddg.Close()
		wiki.Close()
		restoreSearchEndpoints()
	})
	webSearchHTMLEndpoint = ddg.URL
	webSearchEndpoint = ddg.URL
	webSearchWikipediaEndpoint = wiki.URL
	webSearchGitHubEndpoint = ddg.URL

	tool := &webSearchTool{}
	result, err := tool.Run(context.Background(), &Env{}, map[string]any{"query": "go"})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	for _, want := range []string{"wikipedia", "Go (programming language)", "https://en.wikipedia.org/wiki/Go_(programming_language)"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output is missing %q:\n%s", want, result.Output)
		}
	}
}

// TestWebSearchFallsBackToGitHub proves the last source in the chain answers
// when both DuckDuckGo and Wikipedia are down.
func TestWebSearchFallsBackToGitHub(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "blocked", http.StatusForbidden)
	}))
	github := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{"items":[{"full_name":"99apps-id/termixgo","html_url":"https://github.com/99apps-id/termixgo","description":"terminal agent"}]}`)
	}))
	t.Cleanup(func() {
		dead.Close()
		github.Close()
		restoreSearchEndpoints()
	})
	webSearchHTMLEndpoint = dead.URL
	webSearchEndpoint = dead.URL
	webSearchWikipediaEndpoint = dead.URL
	webSearchGitHubEndpoint = github.URL

	tool := &webSearchTool{}
	result, err := tool.Run(context.Background(), &Env{}, map[string]any{"query": "termixgo"})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	for _, want := range []string{"github", "99apps-id/termixgo"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output is missing %q:\n%s", want, result.Output)
		}
	}
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

// TestWebSearchDoesNotClaimOfflineWhenASourceWasReachable is the false-outage
// regression: a blocked DuckDuckGo host must not make an empty result read as
// "the machine is offline" when Wikipedia or GitHub answered.
func TestWebSearchDoesNotClaimOfflineWhenASourceWasReachable(t *testing.T) {
	emptySources(t)
	// A host that never resolves: the DNS failure the operator hits when an
	// ISP blocks DuckDuckGo.
	webSearchHTMLEndpoint = "http://search.invalid/"
	webSearchEndpoint = "http://search.invalid/"
	t.Cleanup(restoreSearchEndpoints)

	tool := &webSearchTool{}
	result, _ := tool.Run(context.Background(), &Env{}, map[string]any{"query": "zzz unlikely"})
	if result.IsError {
		t.Fatalf("a reachable source must not read as offline: %q", result.Output)
	}
	if !strings.Contains(result.Output, "No results") {
		t.Errorf("output = %q, want no results", result.Output)
	}
	if strings.Contains(result.Output, "offline") {
		t.Errorf("output must not claim the machine is offline: %q", result.Output)
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

// TestSearchFailureMentionsDoHWithoutClaimingOffline proves the failure text
// points at the DoH fallback and forbids telling the operator the machine
// cannot search, which is the misreport this change fixes.
func TestSearchFailureMentionsDoHWithoutClaimingOffline(t *testing.T) {
	result := searchFailure("go 1.26", fmt.Errorf("dial tcp: lookup html.duckduckgo.com: no such host"))
	if !result.IsError {
		t.Fatalf("a failed search must be an error result")
	}
	for _, want := range []string{"DNS-over-HTTPS", "Do not tell the operator"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output = %q, want %q", result.Output, want)
		}
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
