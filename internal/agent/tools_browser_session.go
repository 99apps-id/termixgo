package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// browserPageSession is the readable snapshot one browser_session_open call
// keeps for later reads. It reuses the headless DOM dump the project already
// has, so the session composes existing tools instead of driving a browser.
type browserPageSession struct {
	URL      string
	Title    string
	Text     string
	Raw      string
	Elements []browserPageElement
	OpenedAt time.Time
}

type browserPageElement struct {
	Tag         string
	Type        string
	Name        string
	ID          string
	Text        string
	Selector    string
	Placeholder string
}

var (
	browserPageMu      sync.Mutex
	browserPageCurrent *browserPageSession
)

var (
	browserPageTitle  = regexp.MustCompile(`(?i)<title[^>]*>(.*?)</title>`)
	browserPageScript = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	browserPageStyle  = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	browserPageTag    = regexp.MustCompile(`(?s)<[^>]+>`)
	browserPageSpace  = regexp.MustCompile(`[\s]+`)
	browserPageInput  = regexp.MustCompile(`(?i)<input\b([^>]*)>`)
	browserPageButton = regexp.MustCompile(`(?i)<button\b([^>]*)>(.*?)</button>`)
	browserPageAttr   = regexp.MustCompile(`([a-zA-Z0-9_-]+)=["']([^"']*)["']`)
)

// browserSessionOpenTool fetches a page once and keeps its readable snapshot
// for browser_session_read. Interactive pages stay on browser_dump_dom, which
// renders JavaScript the plain fetch below cannot see.
type browserSessionOpenTool struct{}

func (t *browserSessionOpenTool) Name() string { return "browser_session_open" }
func (t *browserSessionOpenTool) Aliases() []string {
	return []string{"browser_session", "open_session"}
}
func (t *browserSessionOpenTool) Mutating() bool { return false }
func (t *browserSessionOpenTool) Risk() Risk     { return RiskNetwork }
func (t *browserSessionOpenTool) Label(a map[string]any) string {
	return "Opening session " + Shorten(argString(a, "url"), 40)
}
func (t *browserSessionOpenTool) DoneLabel(a map[string]any) string {
	return "Opened session " + Shorten(argString(a, "url"), 40)
}
func (t *browserSessionOpenTool) Description() string {
	return "Fetch a page into a readable session snapshot for later reads. For JavaScript pages use browser_dump_dom instead."
}
func (t *browserSessionOpenTool) Schema() map[string]any {
	return object(map[string]any{
		"url": strProp("Full HTTP or HTTPS URL to snapshot (for example https://example.com)."),
	}, "url")
}

func (t *browserSessionOpenTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := strings.TrimSpace(argString(args, "url", "link"))
	if raw == "" {
		return Result{Output: "url is required.", IsError: true}, nil
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return Result{Output: fmt.Sprintf("invalid URL: %s", raw), IsError: true}, nil
	}
	if isBlockedHost(parsed.Hostname()) {
		return Result{Output: "That host is a link-local or cloud metadata address, which the agent does not fetch.", IsError: true}, nil
	}

	fetched, fetchErr := fetchBrowserPage(ctx, raw)
	title := parsed.Host
	if fetched != nil {
		if match := browserPageTitle.FindStringSubmatch(fetched.Raw); len(match) > 1 {
			if named := strings.TrimSpace(match[1]); named != "" {
				title = named
			}
		}
	}
	if fetched == nil {
		fetched = &browserPageSession{URL: raw}
	}
	fetched.URL = raw
	fetched.Title = title
	fetched.OpenedAt = time.Now()

	browserPageMu.Lock()
	browserPageCurrent = fetched
	browserPageMu.Unlock()

	if fetchErr != nil {
		return Result{Output: fmt.Sprintf("Opened %s\nTitle: %s\nThe fetch failed (%v), so the snapshot holds the URL only. For a JavaScript page use browser_dump_dom.", raw, title, fetchErr), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Opened %s\nTitle: %s\nInteractive elements: %d\nExcerpt:\n%s", raw, title, len(fetched.Elements), clipBytes(fetched.Text, 800))}, nil
}

// browserSessionReadTool reads back the snapshot browser_session_open kept.
type browserSessionReadTool struct{}

func (t *browserSessionReadTool) Name() string { return "browser_session_read" }
func (t *browserSessionReadTool) Aliases() []string {
	return []string{"read_session", "browser_snapshot"}
}
func (t *browserSessionReadTool) Mutating() bool { return false }
func (t *browserSessionReadTool) Risk() Risk     { return RiskNetwork }
func (t *browserSessionReadTool) Label(a map[string]any) string {
	return "Reading browser session"
}
func (t *browserSessionReadTool) DoneLabel(a map[string]any) string {
	return "Read browser session"
}
func (t *browserSessionReadTool) Description() string {
	return "Read the page snapshot kept by browser_session_open: text plus form inputs and buttons with selectors."
}
func (t *browserSessionReadTool) Schema() map[string]any {
	return object(map[string]any{
		"include_elements": boolProp("Whether to list interactive form inputs and buttons (default true)."),
	})
}

func (t *browserSessionReadTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	browserPageMu.Lock()
	defer browserPageMu.Unlock()
	if browserPageCurrent == nil {
		return Result{Output: "No browser session is open. Use browser_session_open first.", IsError: true}, nil
	}
	current := browserPageCurrent
	var out strings.Builder
	fmt.Fprintf(&out, "Active page: %s (%s)\nOpened: %s\n\nText:\n%s\n",
		current.Title, current.URL, current.OpenedAt.Format("15:04:05"), clipBytes(current.Text, 2000))
	if argBool(args, "include_elements", true) && len(current.Elements) > 0 {
		out.WriteString("\nInteractive elements:\n")
		for i, el := range current.Elements {
			if i >= 40 {
				fmt.Fprintf(&out, "... (%d more elements elided)\n", len(current.Elements)-40)
				break
			}
			label := el.Text
			if label == "" {
				label = el.Placeholder
			}
			if label == "" {
				label = el.Name
			}
			fmt.Fprintf(&out, "[#%d] <%s type=%q name=%q selector=%q> %s\n", i+1, el.Tag, el.Type, el.Name, el.Selector, label)
		}
	}
	return Result{Output: out.String()}, nil
}

// browserSessionCloseTool drops the snapshot browser_session_open kept.
type browserSessionCloseTool struct{}

func (t *browserSessionCloseTool) Name() string { return "browser_session_close" }
func (t *browserSessionCloseTool) Aliases() []string {
	return []string{"close_session"}
}
func (t *browserSessionCloseTool) Mutating() bool { return false }
func (t *browserSessionCloseTool) Risk() Risk     { return RiskNetwork }
func (t *browserSessionCloseTool) Label(a map[string]any) string {
	return "Closing browser session"
}
func (t *browserSessionCloseTool) DoneLabel(a map[string]any) string {
	return "Closed browser session"
}
func (t *browserSessionCloseTool) Description() string {
	return "Drop the page snapshot kept by browser_session_open."
}
func (t *browserSessionCloseTool) Schema() map[string]any {
	return object(map[string]any{})
}

func (t *browserSessionCloseTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	browserPageMu.Lock()
	defer browserPageMu.Unlock()
	if browserPageCurrent == nil {
		return Result{Output: "No browser session was open."}, nil
	}
	closed := browserPageCurrent.URL
	browserPageCurrent = nil
	return Result{Output: fmt.Sprintf("Closed browser session for %s", closed)}, nil
}

// fetchBrowserPage is the plain HTTP fetch behind the session snapshot. It
// honours the workspace proxy and blocklist through the shared fetch helper,
// so the session cannot reach a host web_fetch itself would refuse.
func fetchBrowserPage(ctx context.Context, raw string) (*browserPageSession, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Termixgo/0.1 (+https://github.com/99apps-id/termixgo)")
	response, err := fetchClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch returned %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	if err != nil {
		return nil, err
	}
	html := string(body)
	text, elements := parseBrowserPage(html)
	return &browserPageSession{Text: text, Raw: html, Elements: elements}, nil
}

// parseBrowserPage reduces fetched HTML to readable text plus the form inputs
// and buttons a later read names with selectors.
func parseBrowserPage(html string) (string, []browserPageElement) {
	if html == "" {
		return "", nil
	}
	clean := browserPageScript.ReplaceAllString(html, " ")
	clean = browserPageStyle.ReplaceAllString(clean, " ")
	clean = browserPageTag.ReplaceAllString(clean, " ")
	plain := strings.TrimSpace(browserPageSpace.ReplaceAllString(clean, " "))
	var elements []browserPageElement
	for _, match := range browserPageInput.FindAllStringSubmatch(html, -1) {
		if len(match) < 2 {
			continue
		}
		attrs := parseBrowserAttrs(match[1])
		if strings.EqualFold(attrs["type"], "hidden") {
			continue
		}
		elements = append(elements, browserPageElement{
			Tag:         "input",
			Type:        attrs["type"],
			Name:        attrs["name"],
			ID:          attrs["id"],
			Placeholder: attrs["placeholder"],
			Selector:    browserSelector("input", attrs),
		})
	}
	for _, match := range browserPageButton.FindAllStringSubmatch(html, -1) {
		if len(match) < 3 {
			continue
		}
		attrs := parseBrowserAttrs(match[1])
		elements = append(elements, browserPageElement{
			Tag:      "button",
			Type:     attrs["type"],
			Name:     attrs["name"],
			ID:       attrs["id"],
			Text:     strings.TrimSpace(browserPageTag.ReplaceAllString(match[2], "")),
			Selector: browserSelector("button", attrs),
		})
	}
	return plain, elements
}

func parseBrowserAttrs(raw string) map[string]string {
	out := map[string]string{}
	for _, match := range browserPageAttr.FindAllStringSubmatch(raw, -1) {
		if len(match) > 2 {
			out[strings.ToLower(match[1])] = match[2]
		}
	}
	return out
}

func browserSelector(tag string, attrs map[string]string) string {
	if id := attrs["id"]; id != "" {
		return "#" + id
	}
	if name := attrs["name"]; name != "" {
		return fmt.Sprintf("%s[name=%q]", tag, name)
	}
	if class := attrs["class"]; class != "" {
		if parts := strings.Fields(class); len(parts) > 0 {
			return tag + "." + parts[0]
		}
	}
	return tag
}
