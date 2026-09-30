package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// webSearchHTMLEndpoint is the keyless results page. DuckDuckGo's instant
// answer API only knows topics it has a card for, so a normal query such as an
// error message returns nothing; the HTML page is the actual search. It is a
// variable so tests can point it at a local server instead of the network.
var webSearchHTMLEndpoint = "https://html.duckduckgo.com/html/"

// webSearchEndpoint is the keyless instant-answer API, kept as a fallback for
// the queries the HTML page refuses (it serves a bot check now and then).
var webSearchEndpoint = "https://api.duckduckgo.com/"

// webSearchWikipediaEndpoint is a second keyless source. Every DuckDuckGo host
// can be unreachable at once, because an ISP or a firewall may block the whole
// domain, and a search that only knows one vendor dies with it. Wikipedia
// answers entity and topic queries from anywhere.
var webSearchWikipediaEndpoint = "https://en.wikipedia.org/w/api.php"

// webSearchGitHubEndpoint is a third keyless source, aimed at the queries a
// coding agent actually makes: an error message, a library or a repository.
var webSearchGitHubEndpoint = "https://api.github.com/search/repositories"

// webSearchSource names where results came from, so the model knows how much
// to trust a general web search against a reference or code lookup.
type webSearchSource string

const (
	sourceDuckDuckGo webSearchSource = "duckduckgo"
	sourceWikipedia  webSearchSource = "wikipedia"
	sourceGitHub     webSearchSource = "github"
)

// webSearchUserAgent identifies the tool. DuckDuckGo's HTML page answers a
// browser-like agent and rejects some empty ones, so the header is explicit.
const webSearchUserAgent = "Mozilla/5.0 (compatible; Termixgo/0.1; +https://github.com/99apps-id/termixgo)"

// webSearchTimeout bounds one search attempt. Discovery should be quick; the
// fetch tool remains for reading whatever the search finds.
const webSearchTimeout = 15 * time.Second

// webSearchTool searches the web and returns titles, URLs and snippets. It
// answers "what changed recently" where web_fetch only reads a known page.
type webSearchTool struct{}

func (t *webSearchTool) Name() string      { return "web_search" }
func (t *webSearchTool) Aliases() []string { return []string{"search_web", "websearch"} }
func (t *webSearchTool) Mutating() bool    { return false }
func (t *webSearchTool) Risk() Risk        { return RiskNetwork }
func (t *webSearchTool) Label(a map[string]any) string {
	return "Searching " + Shorten(argString(a, "query"), 50)
}
func (t *webSearchTool) DoneLabel(a map[string]any) string {
	return "Searched " + Shorten(argString(a, "query"), 50)
}
func (t *webSearchTool) Description() string {
	return "Search the web for documentation, changelogs or error messages and return titles with URLs and snippets. Use it before guessing at a new API, then read the best hit with web_fetch."
}
func (t *webSearchTool) Schema() map[string]any {
	return object(map[string]any{
		"query": strProp("Search query."),
		"count": intProp("Maximum results, 1 to 10. Defaults to 5."),
	}, "query")
}

// searchResult is one hit, ready to render.
type searchResult struct {
	title   string
	url     string
	snippet string
}

// line is the display form handed to the model: title, URL and snippet, each
// on its own line so a URL is never glued to the words around it.
func (r searchResult) line() string {
	parts := []string{r.title}
	if strings.TrimSpace(r.url) != "" {
		parts = append(parts, strings.TrimSpace(r.url))
	}
	if strings.TrimSpace(r.snippet) != "" {
		parts = append(parts, strings.TrimSpace(r.snippet))
	}
	return strings.Join(parts, "\n")
}

func (t *webSearchTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	query := strings.TrimSpace(argString(args, "query"))
	if query == "" {
		return Result{Output: "query is required", IsError: true}, nil
	}
	count := argInt(args, "count", 5, 1, 10)

	requestCtx, cancel := context.WithTimeout(ctx, webSearchTimeout)
	defer cancel()

	results, source, lastErr := searchWeb(requestCtx, query)
	if len(results) == 0 {
		if lastErr != nil {
			return searchFailure(query, lastErr), nil
		}
		return Result{Output: fmt.Sprintf("No results for %q.", query)}, nil
	}
	if len(results) > count {
		results = results[:count]
	}

	lines := make([]string, 0, len(results))
	for _, result := range results {
		lines = append(lines, result.line())
	}
	output := fmt.Sprintf("Source: %s\n\n%s", source, strings.Join(lines, "\n\n"))
	if len(output) > 6000 {
		output = clipBytes(output, 6000) + "\n... [truncated]"
	}
	return Result{Output: output}, nil
}

// searchProvider is one keyless search source in the fallback chain.
type searchProvider struct {
	source webSearchSource
	run    func(context.Context, string) ([]searchResult, error)
}

// searchWeb walks the sources in order and returns the first that answers.
//
// The chain exists because a single vendor is a single point of failure:
// DuckDuckGo can be unreachable in a whole country, and a search tool that
// dies there is worse than one that answers from Wikipedia or GitHub. A
// source that returns no results moves on; the first real error is kept so a
// total outage still reports a cause.
func searchWeb(ctx context.Context, query string) ([]searchResult, webSearchSource, error) {
	providers := []searchProvider{
		{sourceDuckDuckGo, searchDuckDuckGoHTML},
		{sourceDuckDuckGo, searchInstantAnswer},
		{sourceWikipedia, searchWikipedia},
		{sourceGitHub, searchGitHub},
	}
	var firstErr error
	for _, provider := range providers {
		results, err := provider.run(ctx, query)
		if len(results) > 0 {
			return results, provider.source, nil
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return nil, "", firstErr
}

// searchWikipedia queries the MediaWiki opensearch API, which is keyless and
// reachable far more widely than DuckDuckGo. It answers topic and entity
// queries with a title, one-line description and canonical URL each.
func searchWikipedia(ctx context.Context, query string) ([]searchResult, error) {
	endpoint := strings.TrimSpace(webSearchWikipediaEndpoint)
	if endpoint == "" {
		return nil, nil
	}
	link := fmt.Sprintf("%s?action=opensearch&format=json&limit=10&search=%s",
		strings.TrimRight(endpoint, "/"), url.QueryEscape(query))
	body, err := fetchURL(ctx, link)
	if err != nil {
		return nil, err
	}
	var parsed []json.RawMessage
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, fmt.Errorf("read search: %v", err)
	}
	// The payload is [query, [titles], [descriptions], [urls]].
	if len(parsed) < 4 {
		return nil, nil
	}
	var titles, descriptions, urls []string
	_ = json.Unmarshal(parsed[1], &titles)
	_ = json.Unmarshal(parsed[2], &descriptions)
	_ = json.Unmarshal(parsed[3], &urls)
	results := make([]searchResult, 0, len(titles))
	for index, title := range titles {
		snippet := ""
		if index < len(descriptions) {
			snippet = descriptions[index]
		}
		link := ""
		if index < len(urls) {
			link = urls[index]
		}
		results = append(results, searchResult{title: title, url: link, snippet: snippet})
	}
	return results, nil
}

// searchGitHub queries the repository search API, which is keyless at a low
// rate and the right source for a library or an error a project has fixed.
func searchGitHub(ctx context.Context, query string) ([]searchResult, error) {
	endpoint := strings.TrimSpace(webSearchGitHubEndpoint)
	if endpoint == "" {
		return nil, nil
	}
	link := fmt.Sprintf("%s?per_page=10&q=%s", strings.TrimRight(endpoint, "/"), url.QueryEscape(query))
	body, err := fetchURL(ctx, link)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Items []struct {
			FullName    string `json:"full_name"`
			HTMLURL     string `json:"html_url"`
			Description string `json:"description"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, fmt.Errorf("read search: %v", err)
	}
	results := make([]searchResult, 0, len(parsed.Items))
	for _, item := range parsed.Items {
		results = append(results, searchResult{
			title:   item.FullName,
			url:     item.HTMLURL,
			snippet: Shorten(strings.TrimSpace(item.Description), 300),
		})
	}
	return results, nil
}

// searchFailure renders a failed search. A DNS or connectivity failure is
// named as such so the model stops retrying web tools and continues with what
// it has, which is the failure that left it stuck before.
func searchFailure(query string, err error) Result {
	if isNetworkUnreachable(err) {
		return Result{
			Output:  fmt.Sprintf("search failed for %q: %v\nHint: the machine is offline or DNS could not resolve the search host. Do not retry web_search or web_fetch; continue with local repository files, documentation and tools.", query, err),
			IsError: true,
		}
	}
	return Result{Output: fmt.Sprintf("search failed for %q: %v", query, err), IsError: true}
}

// ------------------------------------------------------------------ HTML page

var (
	// ddgResultBlock opens one search result. Splitting on it is more robust
	// than one giant regex: a malformed block cannot swallow the ones after it.
	ddgResultBlock  = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*result[^"]*web-result[^"]*"[^>]*>`)
	ddgAnchor       = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	ddgSnippetBlock = regexp.MustCompile(`(?is)<(?:a|td|div)\b([^>]*)>(.*?)</(?:a|td|div)>`)
	ddgAttribute    = regexp.MustCompile(`(?i)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*"([^"]*)"`)
)

func searchDuckDuckGoHTML(ctx context.Context, query string) ([]searchResult, error) {
	endpoint := strings.TrimSpace(webSearchHTMLEndpoint)
	if endpoint == "" {
		return nil, nil
	}
	body, err := fetchSearchPage(ctx, endpoint, query)
	if err != nil {
		return nil, err
	}
	return parseDuckDuckGoHTML(body), nil
}

// parseDuckDuckGoHTML is deliberately structural and pure: a markup change
// degrades to "no results" rather than an error.
func parseDuckDuckGoHTML(body string) []searchResult {
	blocks := splitResultBlocks(body)
	if len(blocks) == 0 {
		blocks = []string{body}
	}
	var results []searchResult
	for _, block := range blocks {
		title, href, ok := firstResultAnchor(block)
		if !ok {
			continue
		}
		link := decodeDuckDuckGoURL(href)
		if !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
			continue
		}
		results = append(results, searchResult{
			title:   title,
			url:     link,
			snippet: firstResultSnippet(block),
		})
		if len(results) >= 10 {
			break
		}
	}
	return results
}

func splitResultBlocks(body string) []string {
	marks := ddgResultBlock.FindAllStringIndex(body, -1)
	if len(marks) == 0 {
		return nil
	}
	blocks := make([]string, 0, len(marks))
	for index, mark := range marks {
		end := len(body)
		if index+1 < len(marks) {
			end = marks[index+1][0]
		}
		blocks = append(blocks, body[mark[1]:end])
	}
	return blocks
}

func firstResultAnchor(block string) (string, string, bool) {
	for _, match := range ddgAnchor.FindAllStringSubmatch(block, -1) {
		attrs, inner := match[1], match[2]
		if !strings.Contains(strings.ToLower(attributeValue(attrs, "class")), "result__a") {
			continue
		}
		href := attributeValue(attrs, "href")
		title := cleanHTMLText(inner)
		if title == "" || href == "" {
			continue
		}
		return title, href, true
	}
	return "", "", false
}

func firstResultSnippet(block string) string {
	for _, match := range ddgSnippetBlock.FindAllStringSubmatch(block, -1) {
		attrs, inner := match[1], match[2]
		if !strings.Contains(strings.ToLower(attributeValue(attrs, "class")), "result__snippet") {
			continue
		}
		if text := cleanHTMLText(inner); text != "" {
			return Shorten(text, 400)
		}
	}
	return ""
}

// decodeDuckDuckGoURL unwraps the //duckduckgo.com/l/?uddg=<url> redirect a
// result link carries, and passes an already plain URL through untouched.
func decodeDuckDuckGoURL(href string) string {
	raw := strings.TrimSpace(href)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	if !strings.Contains(raw, "uddg=") {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if target := parsed.Query().Get("uddg"); target != "" {
		return target
	}
	return raw
}

func attributeValue(attrs, name string) string {
	want := strings.ToLower(name)
	for _, match := range ddgAttribute.FindAllStringSubmatch(attrs, -1) {
		if strings.ToLower(match[1]) == want {
			return html.UnescapeString(match[2])
		}
	}
	return ""
}

// cleanHTMLText strips tags from a fragment and decodes its entities.
func cleanHTMLText(fragment string) string {
	fragment = tagPattern.ReplaceAllString(fragment, " ")
	fragment = html.UnescapeString(fragment)
	fragment = strings.ReplaceAll(fragment, "\u00a0", " ")
	return strings.TrimSpace(strings.Join(strings.Fields(fragment), " "))
}

// --------------------------------------------------------- instant answer API

type webSearchTopic struct {
	Text     string           `json:"Text"`
	FirstURL string           `json:"FirstURL"`
	Topics   []webSearchTopic `json:"Topics"`
}

type webSearchResponse struct {
	AbstractText string           `json:"AbstractText"`
	AbstractURL  string           `json:"AbstractURL"`
	Related      []webSearchTopic `json:"RelatedTopics"`
}

func searchInstantAnswer(ctx context.Context, query string) ([]searchResult, error) {
	endpoint := strings.TrimSpace(webSearchEndpoint)
	if endpoint == "" {
		return nil, nil
	}
	body, err := fetchSearchPage(ctx, endpoint, query)
	if err != nil {
		return nil, err
	}
	var parsed webSearchResponse
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, fmt.Errorf("read search: %v", err)
	}
	var results []searchResult
	if strings.TrimSpace(parsed.AbstractText) != "" {
		results = append(results, searchResult{title: parsed.AbstractText, url: parsed.AbstractURL})
	}
	results = append(results, flattenSearchTopics(parsed.Related)...)
	return results, nil
}

func flattenSearchTopics(topics []webSearchTopic) []searchResult {
	var results []searchResult
	for _, topic := range topics {
		if len(topic.Topics) > 0 {
			results = append(results, flattenSearchTopics(topic.Topics)...)
			continue
		}
		if strings.TrimSpace(topic.Text) == "" {
			continue
		}
		results = append(results, searchResult{title: topic.Text, url: topic.FirstURL})
	}
	return results
}

// ----------------------------------------------------------------- transport

// fetchSearchPage issues one bounded GET for a page that takes the query in
// its own form, which is how both DuckDuckGo endpoints read it.
func fetchSearchPage(ctx context.Context, endpoint, query string) (string, error) {
	return fetchURL(ctx, appendQuery(endpoint, "q", query))
}

// fetchURL issues one bounded GET and returns the body as text.
func fetchURL(ctx context.Context, link string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", webSearchUserAgent)
	request.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.5")
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("search returned %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read search: %v", err)
	}
	return string(body), nil
}

// appendQuery adds a query parameter without losing an existing query string.
func appendQuery(endpoint, key, value string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	separator := "?"
	if strings.Contains(trimmed, "?") {
		separator = "&"
	}
	return trimmed + separator + key + "=" + url.QueryEscape(value)
}

// isNetworkUnreachable reports whether an error is a DNS or connectivity
// failure, the case where retrying another URL fails the same way.
func isNetworkUnreachable(err error) bool {
	if err == nil {
		return false
	}
	lowered := strings.ToLower(err.Error())
	for _, needle := range []string{
		"no such host",
		"getaddrinfo",
		"temporary failure in name resolution",
		"server misbehaving",
		"network is unreachable",
		"no route to host",
		"connection refused",
		"connection reset",
		"i/o timeout",
		"context deadline exceeded",
	} {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	return false
}
