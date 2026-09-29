package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// webSearchEndpoint is the keyless instant-answer API. It is a variable so
// tests can point it at a local server instead of the network.
var webSearchEndpoint = "https://api.duckduckgo.com/"

// webSearchTimeout bounds one search. Discovery should be quick; the fetch
// tool remains for reading whatever the search finds.
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

func (t *webSearchTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	query := strings.TrimSpace(argString(args, "query"))
	if query == "" {
		return Result{Output: "query is required", IsError: true}, nil
	}
	count := argInt(args, "count", 5, 1, 10)

	endpoint := strings.TrimRight(strings.TrimSpace(webSearchEndpoint), "/")
	link := fmt.Sprintf("%s/?q=%s&format=json&no_html=1&skip_disambig=1", endpoint, url.QueryEscape(query))
	requestCtx, cancel := context.WithTimeout(ctx, webSearchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, link, nil)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	request.Header.Set("User-Agent", "Termixgo/0.1 (+https://github.com/99apps-id/termixgo)")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Result{Output: fmt.Sprintf("search failed: %v", err), IsError: true}, nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{Output: fmt.Sprintf("search returned %s", response.Status), IsError: true}, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if err != nil {
		return Result{Output: fmt.Sprintf("read search: %v", err), IsError: true}, nil
	}
	var parsed webSearchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Result{Output: fmt.Sprintf("read search: %v", err), IsError: true}, nil
	}
	lines := flattenSearchTopics(parsed.Related, count)
	if strings.TrimSpace(parsed.AbstractText) != "" && len(lines) < count {
		lines = append([]string{searchLine(parsed.AbstractText, parsed.AbstractURL)}, lines...)
	}
	if len(lines) == 0 {
		return Result{Output: fmt.Sprintf("No results for %q.", query)}, nil
	}
	if len(lines) > count {
		lines = lines[:count]
	}
	output := strings.Join(lines, "\n\n")
	if len(output) > 6000 {
		output = clipBytes(output, 6000) + "\n... [truncated]"
	}
	return Result{Output: output}, nil
}

func flattenSearchTopics(topics []webSearchTopic, count int) []string {
	var lines []string
	for _, topic := range topics {
		if len(lines) >= count {
			break
		}
		if len(topic.Topics) > 0 {
			lines = append(lines, flattenSearchTopics(topic.Topics, count-len(lines))...)
			continue
		}
		if strings.TrimSpace(topic.Text) == "" {
			continue
		}
		lines = append(lines, searchLine(topic.Text, topic.FirstURL))
	}
	return lines
}

func searchLine(text, link string) string {
	text = strings.TrimSpace(text)
	if strings.TrimSpace(link) == "" {
		return text
	}
	return fmt.Sprintf("%s\n%s", text, strings.TrimSpace(link))
}
