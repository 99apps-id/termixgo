package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// publicURL parses a URL the research tools may fetch and says why it may not
// be, under the same rules as web_fetch.
func publicURL(raw string) (*url.URL, string) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return nil, "url must be an absolute http or https URL"
	}
	if isBlockedHost(parsed.Hostname()) {
		return nil, "That host is a link-local or cloud metadata address, which the agent does not fetch."
	}
	if !allowPrivateFetch() && isPrivateHost(parsed.Hostname()) {
		return nil, "That host is a private or loopback address. The agent fetches public URLs; set TERMIXGO_ALLOW_PRIVATE_FETCH=1 to let it read a private host you run."
	}
	return parsed, ""
}

// ---------------------------------------------------------------- web_extract

// pageInfo is what extractPage learned about an article.
type pageInfo struct {
	URL, Canonical, Title, Author, Published, Modified, Site, Language, Description string
	Text                                                                            string
	Links                                                                           []pageLink
}

type pageLink struct{ Text, URL string }

var (
	metaTagRE   = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	linkTagRE   = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	attrRE      = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	titleRE     = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title\s*>`)
	h1RE        = regexp.MustCompile(`(?is)<h1\b[^>]*>(.*?)</h1\s*>`)
	htmlLangRE  = regexp.MustCompile(`(?is)<html\b[^>]*\blang\s*=\s*["']?([A-Za-z-]+)`)
	ldJSONRE    = regexp.MustCompile(`(?is)<script\b[^>]*application/ld\+json[^>]*>(.*?)</script\s*>`)
	timeTagRE   = regexp.MustCompile(`(?is)<time\b[^>]*\bdatetime\s*=\s*["']([^"']+)["']`)
	anchorRE    = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a\s*>`)
	articleRE   = regexp.MustCompile(`(?is)<article\b[^>]*>.*?</article\s*>`)
	mainRE      = regexp.MustCompile(`(?is)<main\b[^>]*>.*?</main\s*>`)
	bodyRE      = regexp.MustCompile(`(?is)<body\b[^>]*>(.*)</body\s*>`)
	boilerplate = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<nav\b.*?</nav\s*>`),
		regexp.MustCompile(`(?is)<header\b.*?</header\s*>`),
		regexp.MustCompile(`(?is)<footer\b.*?</footer\s*>`),
		regexp.MustCompile(`(?is)<aside\b.*?</aside\s*>`),
		regexp.MustCompile(`(?is)<form\b.*?</form\s*>`),
		regexp.MustCompile(`(?is)<iframe\b.*?</iframe\s*>`),
		regexp.MustCompile(`(?is)<svg\b.*?</svg\s*>`),
		regexp.MustCompile(`(?is)<button\b.*?</button\s*>`),
		regexp.MustCompile(`(?is)<dialog\b.*?</dialog\s*>`),
	}
)

func attrs(tag string) map[string]string {
	out := map[string]string{}
	for _, match := range attrRE.FindAllStringSubmatch(tag, -1) {
		value := match[2]
		if value == "" {
			value = match[3]
		}
		if value == "" {
			value = match[4]
		}
		key := strings.ToLower(match[1])
		if _, seen := out[key]; !seen {
			out[key] = html.UnescapeString(strings.TrimSpace(value))
		}
	}
	return out
}

func cleanText(text string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tagPattern.ReplaceAllString(text, " "))), " ")
}

// extractPage reads the metadata and the main text out of an HTML page. It is
// regex based on purpose: a full HTML parser is a dependency this tool does not
// need for articles, and what it cannot find it leaves empty instead of
// guessing.
func extractPage(raw string, base *url.URL, wantLinks bool) pageInfo {
	info := pageInfo{URL: base.String()}
	meta := map[string]string{}
	for _, tag := range metaTagRE.FindAllString(raw, -1) {
		a := attrs(tag)
		name := strings.ToLower(firstNonEmpty(a["property"], a["name"], a["itemprop"]))
		if name != "" && a["content"] != "" {
			if _, seen := meta[name]; !seen {
				meta[name] = a["content"]
			}
		}
	}
	for _, tag := range linkTagRE.FindAllString(raw, -1) {
		a := attrs(tag)
		if strings.EqualFold(a["rel"], "canonical") && a["href"] != "" {
			if resolved, err := base.Parse(a["href"]); err == nil {
				info.Canonical = resolved.String()
			}
		}
	}
	ld := readLDJSON(raw)

	info.Title = firstNonEmpty(meta["og:title"], meta["twitter:title"], ld["headline"], cleanFirst(titleRE, raw), cleanFirst(h1RE, raw))
	info.Author = firstNonEmpty(meta["author"], meta["article:author"], meta["parsely-author"], meta["dc.creator"], ld["author"])
	info.Published = firstNonEmpty(meta["article:published_time"], meta["og:published_time"], meta["datepublished"], meta["date"], meta["dc.date"], meta["pubdate"], ld["datepublished"])
	if info.Published == "" {
		if match := timeTagRE.FindStringSubmatch(raw); match != nil {
			info.Published = html.UnescapeString(match[1])
		}
	}
	info.Modified = firstNonEmpty(meta["article:modified_time"], meta["og:updated_time"], meta["datemodified"], ld["datemodified"])
	info.Site = firstNonEmpty(meta["og:site_name"], meta["application-name"])
	info.Description = firstNonEmpty(meta["og:description"], meta["description"], meta["twitter:description"])
	if match := htmlLangRE.FindStringSubmatch(raw); match != nil {
		info.Language = match[1]
	}

	cleaned := scriptBlock.ReplaceAllString(raw, " ")
	cleaned = commentBlock.ReplaceAllString(cleaned, " ")
	fragment := mainFragment(cleaned)
	for _, pattern := range boilerplate {
		fragment = pattern.ReplaceAllString(fragment, " ")
	}
	info.Text = htmlToText(fragment)
	if wantLinks {
		info.Links = collectLinks(fragment, base)
	}
	return info
}

func cleanFirst(pattern *regexp.Regexp, raw string) string {
	if match := pattern.FindStringSubmatch(raw); match != nil {
		return cleanText(match[1])
	}
	return ""
}

// mainFragment picks the part of the page that holds the article: the largest
// <article>, else <main>, else the body, else everything.
func mainFragment(cleaned string) string {
	best := ""
	for _, pattern := range []*regexp.Regexp{articleRE, mainRE} {
		for _, match := range pattern.FindAllString(cleaned, -1) {
			if len(cleanText(match)) > len(cleanText(best)) {
				best = match
			}
		}
		if len(cleanText(best)) >= 400 {
			return best
		}
	}
	if match := bodyRE.FindStringSubmatch(cleaned); match != nil {
		return match[1]
	}
	if best != "" {
		return best
	}
	return cleaned
}

// readLDJSON collects the few schema.org fields a citation needs from every
// JSON-LD block. Keys are lower-cased.
func readLDJSON(raw string) map[string]string {
	out := map[string]string{}
	for _, match := range ldJSONRE.FindAllStringSubmatch(raw, -1) {
		var data any
		if json.Unmarshal([]byte(strings.TrimSpace(html.UnescapeString(match[1]))), &data) != nil {
			continue
		}
		walkLD(data, out, 0)
	}
	return out
}

func walkLD(node any, out map[string]string, depth int) {
	if depth > 6 {
		return
	}
	switch typed := node.(type) {
	case []any:
		for _, item := range typed {
			walkLD(item, out, depth+1)
		}
	case map[string]any:
		for _, key := range []string{"headline", "datePublished", "dateModified"} {
			if value, ok := typed[key].(string); ok && value != "" {
				if _, seen := out[strings.ToLower(key)]; !seen {
					out[strings.ToLower(key)] = value
				}
			}
		}
		if _, seen := out["author"]; !seen {
			if names := ldNames(typed["author"]); names != "" {
				out["author"] = names
			}
		}
		if graph, ok := typed["@graph"]; ok {
			walkLD(graph, out, depth+1)
		}
	}
}

func ldNames(node any) string {
	switch typed := node.(type) {
	case string:
		return typed
	case map[string]any:
		if name, ok := typed["name"].(string); ok {
			return name
		}
	case []any:
		var names []string
		for _, item := range typed {
			if name := ldNames(item); name != "" {
				names = append(names, name)
			}
		}
		return strings.Join(names, "; ")
	}
	return ""
}

func collectLinks(fragment string, base *url.URL) []pageLink {
	var links []pageLink
	seen := map[string]bool{}
	for _, match := range anchorRE.FindAllStringSubmatch(fragment, -1) {
		href := attrs("<a " + match[1] + ">")["href"]
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:") || strings.HasPrefix(strings.ToLower(href), "mailto:") {
			continue
		}
		resolved, err := base.Parse(href)
		if err != nil || (resolved.Scheme != "http" && resolved.Scheme != "https") {
			continue
		}
		resolved.Fragment = ""
		target := resolved.String()
		text := cleanText(match[2])
		if seen[target] || text == "" {
			continue
		}
		seen[target] = true
		links = append(links, pageLink{Text: Shorten(text, 80), URL: target})
		if len(links) == 40 {
			break
		}
	}
	return links
}

// fetchPageHTML downloads a page for extraction, under the web_fetch rules for
// hosts and redirects.
func fetchPageHTML(ctx context.Context, raw string) (string, *url.URL, string) {
	parsed, problem := publicURL(raw)
	if problem != "" {
		return "", nil, problem
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", nil, err.Error()
	}
	request.Header.Set("User-Agent", "Termixgo/0.1 (+https://github.com/99apps-id/termixgo)")
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	response, err := fetchClient.Do(request)
	if err != nil {
		return "", nil, "fetch failed: " + err.Error()
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", nil, fmt.Sprintf("fetch returned %s. If the site blocks automated reads, try web_fetch, which can fall back to a reader service.", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 3<<20))
	if err != nil {
		return "", nil, "fetch failed: " + err.Error()
	}
	text := string(body)
	if !looksLikeHtml(response.Header.Get("Content-Type"), text) {
		return "", nil, fmt.Sprintf("This is not an HTML page (%s). Use web_fetch, read_pdf or read_document instead.", response.Header.Get("Content-Type"))
	}
	final := response.Request.URL
	if final == nil {
		final = parsed
	}
	return text, final, ""
}

type webExtractTool struct{}

func (t *webExtractTool) Name() string      { return "web_extract" }
func (t *webExtractTool) Aliases() []string { return []string{"read_article", "article_extract"} }
func (t *webExtractTool) Mutating() bool    { return false }
func (t *webExtractTool) Risk() Risk        { return RiskNetwork }
func (t *webExtractTool) Label(a map[string]any) string {
	return "Extracting " + Shorten(argString(a, "url"), 50)
}
func (t *webExtractTool) DoneLabel(a map[string]any) string {
	return "Extracted " + Shorten(argString(a, "url"), 50)
}
func (t *webExtractTool) Description() string {
	return "Read an article or report page for research: returns its title, author, publication and update dates, site, canonical URL and language, then only the main text without menus, footers and sidebars. Use it when you need to cite a source or read one carefully; the metadata is what cite needs. Fields a page does not declare are left out, never guessed. For a quick look at any URL use web_fetch."
}
func (t *webExtractTool) Schema() map[string]any {
	return object(map[string]any{
		"url":       strProp("Absolute http or https URL of the page."),
		"max_chars": intProp("Longest main text to return, 1000 to 40000. Default 12000."),
		"links":     boolProp("Also list the links inside the main text. Default false."),
	}, "url")
}

func (t *webExtractTool) Run(ctx context.Context, _ *Env, args map[string]any) (Result, error) {
	raw, base, problem := fetchPageHTML(ctx, argString(args, "url"))
	if problem != "" {
		return Result{Output: problem, IsError: true}, nil
	}
	info := extractPage(raw, base, argBool(args, "links", false))
	limit := argInt(args, "max_chars", 12000, 1000, 40000)
	text := info.Text
	truncated := false
	if len(text) > limit {
		text = clipBytes(text, limit)
		truncated = true
	}
	var out strings.Builder
	line := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&out, "%s: %s\n", label, value)
		}
	}
	line("Title", info.Title)
	line("Author", info.Author)
	line("Published", info.Published)
	line("Updated", info.Modified)
	line("Site", info.Site)
	line("Language", info.Language)
	line("URL", info.URL)
	if info.Canonical != "" && info.Canonical != info.URL {
		line("Canonical", info.Canonical)
	}
	line("Retrieved", time.Now().UTC().Format("2006-01-02"))
	if strings.TrimSpace(info.Text) == "" {
		out.WriteString("\nNo readable text was found. The page may be built by JavaScript; try web_fetch with reader=true, or the browser tools.\n")
	} else {
		fmt.Fprintf(&out, "Length: %d words\n\n%s", len(strings.Fields(info.Text)), text)
		if truncated {
			fmt.Fprintf(&out, "\n... [text cut at %d characters; raise max_chars to read more]", limit)
		}
	}
	if len(info.Links) > 0 {
		out.WriteString("\n\nLinks in the text:\n")
		for _, link := range info.Links {
			fmt.Fprintf(&out, "- %s: %s\n", link.Text, link.URL)
		}
	}
	return Result{Output: strings.TrimRight(out.String(), "\n"), Display: Shorten(info.Title, 60)}, nil
}

// ---------------------------------------------------------------- cite

const sourcesFile = "sources.jsonl"

var sourcesMu sync.Mutex

type sourceEntry struct {
	ID         int    `json:"id"`
	URL        string `json:"url,omitempty"`
	Title      string `json:"title"`
	Author     string `json:"author,omitempty"`
	Published  string `json:"published,omitempty"`
	Site       string `json:"site,omitempty"`
	Accessed   string `json:"accessed"`
	Quote      string `json:"quote,omitempty"`
	Note       string `json:"note,omitempty"`
	ArchiveURL string `json:"archive_url,omitempty"`
}

func sourcesPath(env *Env) string {
	root := ""
	if env != nil {
		root = env.Workspace
	}
	return filepath.Join(root, ".termixgo", sourcesFile)
}

func readSources(path string) ([]sourceEntry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []sourceEntry
	for index, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry sourceEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("line %d of the source list is not valid: %w", index+1, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func writeSources(path string, entries []sourceEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var out strings.Builder
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		out.Write(data)
		out.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(out.String()), 0o644)
}

type citeTool struct{}

func (t *citeTool) Name() string      { return "cite" }
func (t *citeTool) Aliases() []string { return []string{"sources", "bibliography"} }
func (t *citeTool) Mutating() bool    { return true }
func (t *citeTool) Risk() Risk        { return RiskEdit }
func (t *citeTool) Label(a map[string]any) string {
	return "Sources: " + citeAction(a)
}
func (t *citeTool) DoneLabel(a map[string]any) string {
	return "Sources: " + citeAction(a)
}
func citeAction(a map[string]any) string {
	if action := strings.ToLower(strings.TrimSpace(argString(a, "action"))); action != "" {
		return action
	}
	return "list"
}
func (t *citeTool) Description() string {
	return "Keep the sources of a piece of research and turn them into a reference list. add stores a source with its URL, title, author, publication date, the date it was accessed, an exact quote and an archive link; given only a URL it reads the title, author and date from the page. list shows the sources with their numbers, export writes the list as apa, bibtex or markdown, remove drops one. Cite as [n] in the text. Never invent an author, a date or a quote: leave a field empty when the page does not say."
}
func (t *citeTool) Schema() map[string]any {
	return object(map[string]any{
		"action":      strProp("add, list, export or remove. Default list."),
		"url":         strProp("add: the source URL."),
		"title":       strProp("add: title, when the page does not give one or you want to override it."),
		"author":      strProp("add: author or organisation as the source states it."),
		"published":   strProp("add: publication date as the source states it, ideally 2026-11-22."),
		"site":        strProp("add: publisher or site name."),
		"quote":       strProp("add: the exact words relied on, copied from the source."),
		"note":        strProp("add: why this source matters."),
		"archive_url": strProp("add: a snapshot link, for example from wayback."),
		"fetch":       boolProp("add: read missing title, author and date from the URL. Default true."),
		"id":          intProp("remove: number of the source."),
		"format":      strProp("export: apa, bibtex or markdown. Default apa."),
		"path":        strProp("export: file to write in the workspace. Without it the text is returned."),
	})
}

func (t *citeTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	sourcesMu.Lock()
	defer sourcesMu.Unlock()
	path := sourcesPath(env)
	entries, err := readSources(path)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	switch citeAction(args) {
	case "add":
		return t.add(ctx, path, entries, args)
	case "list":
		return t.list(entries), nil
	case "export":
		return t.export(env, entries, args)
	case "remove":
		return t.remove(path, entries, args)
	}
	return Result{Output: "action must be add, list, export or remove", IsError: true}, nil
}

func (t *citeTool) add(ctx context.Context, path string, entries []sourceEntry, args map[string]any) (Result, error) {
	entry := sourceEntry{
		URL:        strings.TrimSpace(argString(args, "url")),
		Title:      strings.TrimSpace(argString(args, "title")),
		Author:     strings.TrimSpace(argString(args, "author")),
		Published:  strings.TrimSpace(argString(args, "published")),
		Site:       strings.TrimSpace(argString(args, "site")),
		Quote:      strings.TrimSpace(argString(args, "quote")),
		Note:       strings.TrimSpace(argString(args, "note")),
		ArchiveURL: strings.TrimSpace(argString(args, "archive_url")),
		Accessed:   time.Now().UTC().Format("2006-01-02"),
	}
	if entry.URL == "" && entry.Title == "" {
		return Result{Output: "give a url, or at least a title", IsError: true}, nil
	}
	for _, existing := range entries {
		if entry.URL != "" && existing.URL == entry.URL {
			return Result{Output: fmt.Sprintf("Source [%d] already holds this URL: %s", existing.ID, existing.Title), Display: fmt.Sprintf("[%d]", existing.ID)}, nil
		}
	}
	notes := ""
	needsFetch := entry.URL != "" && (entry.Title == "" || entry.Author == "" || entry.Published == "")
	if needsFetch && argBool(args, "fetch", true) {
		raw, base, problem := fetchPageHTML(ctx, entry.URL)
		if problem != "" {
			notes = "\nThe page could not be read for details (" + Shorten(problem, 80) + "). Fill title, author and date yourself from the source."
		} else {
			info := extractPage(raw, base, false)
			entry.Title = firstNonEmpty(entry.Title, info.Title)
			entry.Author = firstNonEmpty(entry.Author, info.Author)
			entry.Published = firstNonEmpty(entry.Published, info.Published)
			entry.Site = firstNonEmpty(entry.Site, info.Site)
		}
	}
	if entry.Title == "" {
		entry.Title = entry.URL
	}
	next := 1
	for _, existing := range entries {
		if existing.ID >= next {
			next = existing.ID + 1
		}
	}
	entry.ID = next
	if err := writeSources(path, append(entries, entry)); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	var missing []string
	if entry.Author == "" {
		missing = append(missing, "author")
	}
	if entry.Published == "" {
		missing = append(missing, "publication date")
	}
	if len(missing) > 0 {
		notes += "\nNot found in the source: " + strings.Join(missing, ", ") + ". It is left empty rather than guessed."
	}
	return Result{Output: fmt.Sprintf("Added source [%d]: %s%s", entry.ID, entry.Title, notes), Display: fmt.Sprintf("[%d]", entry.ID)}, nil
}

func (t *citeTool) list(entries []sourceEntry) Result {
	if len(entries) == 0 {
		return Result{Output: "No sources yet."}
	}
	var out strings.Builder
	for _, entry := range entries {
		fmt.Fprintf(&out, "[%d] %s", entry.ID, entry.Title)
		if byline := joinNonEmpty(", ", entry.Author, entry.Published); byline != "" {
			fmt.Fprintf(&out, " (%s)", byline)
		}
		out.WriteString("\n")
		if entry.URL != "" {
			fmt.Fprintf(&out, "    %s, accessed %s\n", entry.URL, entry.Accessed)
		}
		if entry.Quote != "" {
			fmt.Fprintf(&out, "    quote: %s\n", Shorten(entry.Quote, 160))
		}
	}
	return Result{Output: strings.TrimRight(out.String(), "\n")}
}

func (t *citeTool) remove(path string, entries []sourceEntry, args map[string]any) (Result, error) {
	id := argInt(args, "id", 0, 0, 1<<30)
	var kept []sourceEntry
	found := false
	for _, entry := range entries {
		if entry.ID == id {
			found = true
			continue
		}
		kept = append(kept, entry)
	}
	if !found {
		return Result{Output: fmt.Sprintf("no source numbered %d", id), IsError: true}, nil
	}
	if err := writeSources(path, kept); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Removed source [%d]. The other numbers are unchanged.", id)}, nil
}

func (t *citeTool) export(env *Env, entries []sourceEntry, args map[string]any) (Result, error) {
	if len(entries) == 0 {
		return Result{Output: "No sources yet."}, nil
	}
	format := strings.ToLower(strings.TrimSpace(argString(args, "format")))
	if format == "" {
		format = "apa"
	}
	sorted := append([]sourceEntry(nil), entries...)
	var text string
	switch format {
	case "apa":
		sort.SliceStable(sorted, func(i, j int) bool {
			return strings.ToLower(joinNonEmpty(" ", sorted[i].Author, sorted[i].Title)) < strings.ToLower(joinNonEmpty(" ", sorted[j].Author, sorted[j].Title))
		})
		lines := make([]string, len(sorted))
		for index, entry := range sorted {
			lines[index] = formatAPA(entry)
		}
		text = strings.Join(lines, "\n\n")
	case "bibtex":
		lines := make([]string, len(sorted))
		for index, entry := range sorted {
			lines[index] = formatBibTeX(entry)
		}
		text = strings.Join(lines, "\n\n")
	case "markdown":
		var out strings.Builder
		for _, entry := range sorted {
			fmt.Fprintf(&out, "%d. %s", entry.ID, formatAPA(entry))
			if entry.Quote != "" {
				fmt.Fprintf(&out, "\n   > %s", strings.ReplaceAll(entry.Quote, "\n", " "))
			}
			out.WriteString("\n")
		}
		text = strings.TrimRight(out.String(), "\n")
	default:
		return Result{Output: "format must be apa, bibtex or markdown", IsError: true}, nil
	}
	if target := strings.TrimSpace(argString(args, "path")); target != "" {
		resolved := resolvePath(env, target)
		if err := checkWorkspacePath(env, resolved); err != nil {
			return Result{Output: "path must be inside the workspace: " + err.Error(), IsError: true}, nil
		}
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		if err := os.WriteFile(resolved, []byte(text+"\n"), 0o644); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		return Result{Output: fmt.Sprintf("Wrote %d sources to %s as %s.", len(sorted), displayPath(env, resolved), format)}, nil
	}
	return Result{Output: text}, nil
}

var monthNames = [...]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}

// apaDate turns a date as a source states it into APA's "2026, November 22".
// A date it cannot read is kept as written.
func apaDate(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "n.d."
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02", "2006-01", "2006"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			switch layout {
			case "2006":
				return fmt.Sprintf("%d", parsed.Year())
			case "2006-01":
				return fmt.Sprintf("%d, %s", parsed.Year(), monthNames[parsed.Month()-1])
			}
			return fmt.Sprintf("%d, %s %d", parsed.Year(), monthNames[parsed.Month()-1], parsed.Day())
		}
	}
	return text
}

func formatAPA(entry sourceEntry) string {
	var out strings.Builder
	if entry.Author != "" {
		fmt.Fprintf(&out, "%s. (%s). %s.", strings.TrimRight(entry.Author, "."), apaDate(entry.Published), strings.TrimRight(entry.Title, "."))
	} else {
		fmt.Fprintf(&out, "%s. (%s).", strings.TrimRight(entry.Title, "."), apaDate(entry.Published))
	}
	if entry.Site != "" && !strings.EqualFold(entry.Site, entry.Author) {
		fmt.Fprintf(&out, " %s.", strings.TrimRight(entry.Site, "."))
	}
	if entry.URL != "" {
		fmt.Fprintf(&out, " %s", entry.URL)
	}
	fmt.Fprintf(&out, " (accessed %s)", entry.Accessed)
	if entry.ArchiveURL != "" {
		fmt.Fprintf(&out, " Archived: %s", entry.ArchiveURL)
	}
	return out.String()
}

var bibtexEscape = strings.NewReplacer("&", `\&`, "%", `\%`, "_", `\_`, "#", `\#`, "{", `\{`, "}", `\}`)

func formatBibTeX(entry sourceEntry) string {
	year := ""
	if len(entry.Published) >= 4 {
		year = entry.Published[:4]
	}
	label := "source"
	if entry.Author != "" {
		fields := strings.FieldsFunc(entry.Author, func(r rune) bool { return r == ' ' || r == ',' || r == ';' })
		if len(fields) > 0 {
			label = fields[len(fields)-1]
			if strings.Contains(entry.Author, ",") {
				label = fields[0]
			}
		}
	} else if parsed, err := url.Parse(entry.URL); err == nil && parsed.Hostname() != "" {
		label = strings.TrimPrefix(parsed.Hostname(), "www.")
	}
	key := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, strings.ToLower(label)) + year + fmt.Sprintf("_%d", entry.ID)
	var out strings.Builder
	fmt.Fprintf(&out, "@misc{%s,\n  title = {%s},\n", key, bibtexEscape.Replace(entry.Title))
	if entry.Author != "" {
		fmt.Fprintf(&out, "  author = {%s},\n", bibtexEscape.Replace(entry.Author))
	}
	if year != "" {
		fmt.Fprintf(&out, "  year = {%s},\n", year)
	}
	if entry.Site != "" {
		fmt.Fprintf(&out, "  howpublished = {%s},\n", bibtexEscape.Replace(entry.Site))
	}
	if entry.URL != "" {
		fmt.Fprintf(&out, "  url = {%s},\n", entry.URL)
	}
	note := "Accessed " + entry.Accessed
	if entry.ArchiveURL != "" {
		note += ". Archived at " + entry.ArchiveURL
	}
	fmt.Fprintf(&out, "  note = {%s}\n}", bibtexEscape.Replace(note))
	return out.String()
}

// ---------------------------------------------------------------- wayback

var (
	waybackAPI = "https://archive.org"
	waybackWeb = "https://web.archive.org"
)

var waybackClient = &http.Client{Transport: webSearchClient.Transport, Timeout: 100 * time.Second}

type waybackTool struct{}

func (t *waybackTool) Name() string      { return "wayback" }
func (t *waybackTool) Aliases() []string { return []string{"web_archive", "archive_page"} }
func (t *waybackTool) Mutating() bool    { return true }
func (t *waybackTool) Risk() Risk        { return RiskNetwork }
func (t *waybackTool) Label(a map[string]any) string {
	return "Internet Archive " + waybackAction(a) + ": " + Shorten(argString(a, "url"), 40)
}
func (t *waybackTool) DoneLabel(a map[string]any) string {
	return "Internet Archive " + waybackAction(a) + ": " + Shorten(argString(a, "url"), 40)
}
func waybackAction(a map[string]any) string {
	if action := strings.ToLower(strings.TrimSpace(argString(a, "action"))); action != "" {
		return action
	}
	return "check"
}
func (t *waybackTool) Description() string {
	return "Use the Internet Archive so a source survives being edited or deleted. check finds the snapshot closest to a date (or the latest), list shows the recent distinct snapshots of a URL, and save asks the archive to capture the page now and returns the permanent link. Save publishes the URL to a third party: do not save a private, internal or confidential address."
}
func (t *waybackTool) Schema() map[string]any {
	return object(map[string]any{
		"url":       strProp("The page address."),
		"action":    strProp("check, list or save. Default check."),
		"timestamp": strProp("check: the date to find the closest snapshot to, as YYYYMMDD or YYYYMMDDhhmmss. Default: the latest."),
		"limit":     intProp("list: how many snapshots, 1 to 50. Default 10."),
	}, "url")
}

func (t *waybackTool) Run(ctx context.Context, _ *Env, args map[string]any) (Result, error) {
	target, problem := publicURL(argString(args, "url"))
	if problem != "" {
		return Result{Output: problem, IsError: true}, nil
	}
	switch waybackAction(args) {
	case "check":
		return waybackCheck(ctx, target.String(), strings.TrimSpace(argString(args, "timestamp")))
	case "list":
		return waybackList(ctx, target.String(), argInt(args, "limit", 10, 1, 50))
	case "save":
		return waybackSave(ctx, target.String())
	}
	return Result{Output: "action must be check, list or save", IsError: true}, nil
}

func waybackGet(ctx context.Context, endpoint string) ([]byte, http.Header, int, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, 0, "", err
	}
	request.Header.Set("User-Agent", "Termixgo/0.1 (+https://github.com/99apps-id/termixgo)")
	response, err := waybackClient.Do(request)
	if err != nil {
		return nil, nil, 0, "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	final := ""
	if response.Request != nil && response.Request.URL != nil {
		final = response.Request.URL.String()
	}
	return body, response.Header, response.StatusCode, final, err
}

// snapshotDate writes an archive timestamp as a date.
func snapshotDate(timestamp string) string {
	if parsed, err := time.Parse("20060102150405", timestamp); err == nil {
		return parsed.Format("2006-01-02 15:04 UTC")
	}
	return timestamp
}

func waybackCheck(ctx context.Context, target, timestamp string) (Result, error) {
	endpoint := waybackAPI + "/wayback/available?url=" + url.QueryEscape(target)
	if timestamp != "" {
		endpoint += "&timestamp=" + url.QueryEscape(timestamp)
	}
	body, _, status, _, err := waybackGet(ctx, endpoint)
	if err != nil {
		return Result{Output: "The Internet Archive could not be reached: " + err.Error(), IsError: true}, nil
	}
	if status != http.StatusOK {
		return Result{Output: fmt.Sprintf("The Internet Archive answered %d.", status), IsError: true}, nil
	}
	var parsed struct {
		Snapshots struct {
			Closest struct {
				Available bool   `json:"available"`
				URL       string `json:"url"`
				Timestamp string `json:"timestamp"`
				Status    string `json:"status"`
			} `json:"closest"`
		} `json:"archived_snapshots"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Result{Output: "The Internet Archive's answer could not be read.", IsError: true}, nil
	}
	closest := parsed.Snapshots.Closest
	if !closest.Available || closest.URL == "" {
		return Result{Output: "No snapshot of this URL is in the Internet Archive. Use action=save to capture it now."}, nil
	}
	snapshot := strings.Replace(closest.URL, "http://", "https://", 1)
	return Result{Output: fmt.Sprintf("Closest snapshot: %s\nTaken: %s (HTTP %s)", snapshot, snapshotDate(closest.Timestamp), closest.Status), Display: snapshotDate(closest.Timestamp)}, nil
}

func waybackList(ctx context.Context, target string, limit int) (Result, error) {
	endpoint := fmt.Sprintf("%s/cdx/search/cdx?url=%s&output=json&fl=timestamp,statuscode,original&filter=statuscode:200&collapse=digest&limit=-%d", waybackWeb, url.QueryEscape(target), limit)
	body, _, status, _, err := waybackGet(ctx, endpoint)
	if err != nil {
		return Result{Output: "The Internet Archive could not be reached: " + err.Error(), IsError: true}, nil
	}
	if status != http.StatusOK {
		return Result{Output: fmt.Sprintf("The Internet Archive answered %d.", status), IsError: true}, nil
	}
	var rows [][]string
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &rows); err != nil {
			return Result{Output: "The Internet Archive's answer could not be read.", IsError: true}, nil
		}
	}
	if len(rows) > 0 {
		rows = rows[1:]
	}
	if len(rows) == 0 {
		return Result{Output: "No snapshot of this URL is in the Internet Archive."}, nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%d most recent distinct snapshot(s), newest last:\n", len(rows))
	for _, row := range rows {
		if len(row) < 3 {
			continue
		}
		fmt.Fprintf(&out, "- %s  %s/web/%s/%s\n", snapshotDate(row[0]), waybackWeb, row[0], row[2])
	}
	return Result{Output: strings.TrimRight(out.String(), "\n")}, nil
}

var snapshotPathRE = regexp.MustCompile(`/web/(\d{14})/`)

func waybackSave(ctx context.Context, target string) (Result, error) {
	_, headers, status, final, err := waybackGet(ctx, waybackWeb+"/save/"+target)
	if err != nil {
		return Result{Output: "The Internet Archive could not be reached: " + err.Error() + ". Saving can take a minute; try check afterwards in case the capture went through.", IsError: true}, nil
	}
	location := firstNonEmpty(headers.Get("Content-Location"), final)
	if match := snapshotPathRE.FindStringSubmatch(location); match != nil && status >= 200 && status < 300 {
		link := location
		if strings.HasPrefix(link, "/") {
			link = waybackWeb + link
		}
		return Result{Output: fmt.Sprintf("Saved. Snapshot of %s taken %s:\n%s", target, snapshotDate(match[1]), link), Display: snapshotDate(match[1])}, nil
	}
	if status == http.StatusTooManyRequests {
		return Result{Output: "The Internet Archive is rate limiting captures. Wait a few minutes and try again.", IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("The Internet Archive did not confirm a capture (HTTP %d). Try check in a minute; if there is still no snapshot the site may block archiving.", status), IsError: true}, nil
}
