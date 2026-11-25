package agent

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/browser"
)

// renderMarkdownTool generates a standalone dark-themed HTML file from
// Markdown (inline text or a workspace file) and displays it in the browser.
// It covers the same ground as render_mermaid for prose documents, tables and
// fenced code blocks: embedded Mermaid blocks are passed through to the
// pinned Mermaid renderer the same way.
type renderMarkdownTool struct{}

func (t *renderMarkdownTool) Name() string { return "render_markdown" }
func (t *renderMarkdownTool) Aliases() []string {
	return []string{"markdown_render", "preview_markdown", "render_md", "markdown_preview"}
}
func (t *renderMarkdownTool) Mutating() bool { return true }
func (t *renderMarkdownTool) Risk() Risk     { return RiskEdit }
func (t *renderMarkdownTool) Label(a map[string]any) string {
	if title := argString(a, "title"); title != "" {
		return "Rendering Markdown " + Shorten(title, 30)
	}
	if path := argString(a, "path"); path != "" {
		return "Rendering Markdown " + Shorten(path, 30)
	}
	return "Rendering Markdown"
}
func (t *renderMarkdownTool) DoneLabel(a map[string]any) string {
	return "Rendered Markdown"
}
func (t *renderMarkdownTool) Description() string {
	return "Render Markdown text or a workspace Markdown file into a standalone dark-themed HTML page and open it in the default web browser. Supported: headings, bold/italic, inline code, fenced code blocks (including mermaid), lists, task lists, tables, blockquotes, links, images and horizontal rules."
}
func (t *renderMarkdownTool) Schema() map[string]any {
	return object(map[string]any{
		"text":         strProp("Markdown content to render. Takes precedence over path."),
		"path":         strProp("Optional workspace Markdown file path (.md/.markdown)."),
		"title":        strProp("Optional page title."),
		"open_browser": boolProp("Whether to launch the browser immediately (default true)."),
		"output_path":  strProp("Optional custom output path for the HTML file (defaults to '.termixgo/previews/<name>.html')."),
	})
}

func (t *renderMarkdownTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	text := strings.TrimSpace(argString(args, "text"))
	title := strings.TrimSpace(argString(args, "title"))
	sourceName := "document"
	if text == "" {
		rawPath := strings.TrimSpace(argString(args, "path"))
		if rawPath == "" {
			return Result{Output: "text or path is required.", IsError: true}, nil
		}
		resolved := resolvePath(env, rawPath)
		if err := checkWorkspacePath(env, resolved); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		sourceName = filepath.Base(rawPath)
		data, err := os.ReadFile(resolved)
		if err != nil {
			return Result{Output: fmt.Sprintf("cannot read Markdown file: %v", err), IsError: true}, nil
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		return Result{Output: "markdown content is required.", IsError: true}, nil
	}

	var outPath string
	if rawOut := strings.TrimSpace(argString(args, "output_path")); rawOut != "" {
		outPath = resolvePath(env, rawOut)
		if err := checkWorkspacePath(env, outPath); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
	} else {
		name := strings.TrimSuffix(sourceName, filepath.Ext(sourceName))
		name = strings.ReplaceAll(name, " ", "-")
		if strings.TrimSpace(name) == "" {
			name = "document"
		}
		outPath = filepath.Join(env.Workspace, ".termixgo", "previews", name+"-"+time.Now().Format("20060102-150405")+".html")
	}
	_ = os.MkdirAll(filepath.Dir(outPath), 0o755)

	pageTitle := title
	if pageTitle == "" {
		pageTitle = firstMarkdownHeading(text)
	}
	if pageTitle == "" {
		pageTitle = "Markdown"
	}
	content := buildMarkdownHTML(pageTitle, text)
	if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
		return Result{Output: fmt.Sprintf("Failed to write HTML file: %v", err), IsError: true}, nil
	}

	relPath := displayPath(env, outPath)

	// Display through the system browser, and reflect through the live one.
	shouldOpen := true
	if v, ok := args["open_browser"].(bool); ok {
		shouldOpen = v
	}
	if shouldOpen {
		if err := launchDefaultBrowser(outPath); err != nil {
			// A default-browser failure goes to the live browser instead.
			select {
			case <-ctx.Done():
				return Result{Output: fmt.Sprintf("Markdown HTML generated at: %s (browser launch failed: %v)", relPath, err), IsError: true}, nil
			default:
			}
		} else {
			return Result{Output: fmt.Sprintf("Markdown rendered and opened in browser.\nFile: %s", relPath)}, nil
		}
		if browser.IsOpen() {
			runCtx, cancel := context.WithTimeout(ctx, browserActionTimeout)
			defer cancel()
			if session, err := browser.Open(runCtx); err == nil {
				if _, err := session.Navigate(runCtx, outPath); err == nil {
					return Result{Output: fmt.Sprintf("Markdown rendered in the live browser.\nFile: %s", relPath)}, nil
				}
			}
		}
		return Result{Output: fmt.Sprintf("Markdown HTML generated at: %s (could not open a browser)", relPath), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Markdown HTML generated at: %s", relPath)}, nil
}

// firstMarkdownHeading returns the first ATX heading text, so a file without
// an explicit title still names its page.
func firstMarkdownHeading(markdown string) string {
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			return strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		}
	}
	return ""
}

// markdownFenceRE matches an opening fence line with its info string.
var markdownFenceRE = regexp.MustCompile("^```([^`]*)$")

// markdownHeadingRE matches ATX headings.
var markdownHeadingRE = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)

var markdownInlineTokenRE = regexp.MustCompile("`([^`]+)`" +
	`|(!?)\[([^\]]*)\]\(([^)\s]+)(?: "[^"]*")?\)`)

// markdownRuleRE matches horizontal rules.
// markdownRuleRE matches horizontal rules: ---, *** or ___ (compact: tests use ---).
var markdownRuleRE = regexp.MustCompile(`^(-{3,}$|\*{3,}$|_{3,}$)`)

// markdownOrderedRE matches ordered list items.
var markdownOrderedRE = regexp.MustCompile(`^\s*\d+\.\s+(.*)$`)

// markdownUnorderedRE matches unordered list items.
var markdownUnorderedRE = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)

// markdownTaskRE matches task list items.
var markdownTaskRE = regexp.MustCompile(`^\s*[-*+]\s+\[([ xX])\]\s+(.*)$`)

// markdownTableRE matches table rows.
var markdownTableRE = regexp.MustCompile(`^\s*\|.*\|\s*$`)

// markdownTableSepRE matches the header separator row.
var markdownTableSepRE = regexp.MustCompile(`^\s*\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)+\|?\s*$`)

// buildMarkdownHTML converts Markdown to a standalone dark-themed HTML page.
// Embedded Mermaid code blocks are handed to the pinned Mermaid renderer the
// same way render_mermaid embeds them.
func buildMarkdownHTML(title, markdown string) string {
	body := renderMarkdownBody(markdown)
	escapedTitle := html.EscapeString(title)
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>
  * { box-sizing: border-box; }
  body {
    margin: 0; background: #0f141c; color: #e2e8f0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    padding: 32px 16px; line-height: 1.6;
  }
  .wrap { max-width: 960px; margin: 0 auto; }
  h1,h2,h3,h4,h5,h6 { color: #38bdf8; margin: 1.2em 0 0.6em; line-height: 1.3; }
  h1 { font-size: 2rem; border-bottom: 1px solid #1e293b; padding-bottom: 8px; }
  h2 { font-size: 1.5rem; }
  h3 { font-size: 1.2rem; }
  p { margin: 0.8em 0; }
  a { color: #7dd3fc; }
  code { background: #1e293b; padding: 1px 5px; border-radius: 4px; font-family: "Cascadia Code", Consolas, monospace; font-size: 0.85em; }
  pre { background: #151d2a; border: 1px solid #1e293b; border-radius: 6px; padding: 14px; overflow: auto; }
  pre code { background: transparent; padding: 0; }
  .mermaid { text-align: center; }
  blockquote { border-left: 3px solid #38bdf8; margin: 1em 0; padding: 4px 16px; color: #94a3b8; background: #151d2a; }
  ul,ol { padding-left: 1.6em; }
  li { margin: 2px 0; }
  table { border-collapse: collapse; width: 100%%; margin: 1em 0; }
  th,td { border: 1px solid #1e293b; padding: 6px 10px; text-align: left; }
  th { background: #151d2a; }
  hr { border: none; border-top: 1px solid #1e293b; margin: 1.6em 0; }
  img { max-width: 100%%; }
</style>
<script src="%s" integrity="%s" crossorigin="anonymous"></script>
<script>
  if (window.mermaid) {
    mermaid.initialize({ startOnLoad: true, theme: 'dark' });
  }
</script>
</head>
<body>
<div class="wrap">
%s
</div>
</body>
</html>`, escapedTitle, mermaidCDNURL, mermaidSRIIntegrity, body)
}

// renderMarkdownBody converts Markdown source to HTML body markup.
func renderMarkdownBody(markdown string) string {
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")
	lines := strings.Split(markdown, "\n")
	var out strings.Builder
	inList := false
	inOrdered := false
	inTable := false
	inCodeFence := false
	codeLang := ""
	var codeBuf strings.Builder
	closeList := func() {
		if inOrdered {
			out.WriteString("</ol>\n")
		} else if inList {
			out.WriteString("</ul>\n")
		}
		inList, inOrdered = false, false
	}
	closeTable := func() {
		if inTable {
			out.WriteString("</tbody></table>\n")
		}
		inTable = false
	}
	for _, raw := range lines {
		line := strings.TrimRight(raw, " ")
		trimmed := strings.TrimSpace(line)
		if !inCodeFence {
			if fence := markdownFenceRE.FindStringSubmatch(trimmed); fence != nil {
				closeList()
				closeTable()
				inCodeFence = true
				codeLang = strings.TrimSpace(fence[1])
				codeBuf.Reset()
				continue
			}
		}
		if inCodeFence {
			if trimmed == "```" {
				inCodeFence = false
				if codeLang == "mermaid" {
					out.WriteString(`<pre class="mermaid">` + "\n" + html.EscapeString(codeBuf.String()) + "</pre>\n")
				} else {
					out.WriteString("<pre><code>" + html.EscapeString(codeBuf.String()) + "</code></pre>\n")
				}
				continue
			}
			codeBuf.WriteString(line + "\n")
			continue
		}
		if matches := markdownHeadingRE.FindStringSubmatch(trimmed); matches != nil {
			closeList()
			closeTable()
			level := len(matches[1])
			out.WriteString(fmt.Sprintf("<h%d>%s</h%d>\n", level, renderMarkdownInline(matches[2]), level))
			continue
		}
		if markdownRuleRE.MatchString(trimmed) {
			closeList()
			closeTable()
			out.WriteString("<hr>\n")
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			closeList()
			closeTable()
			quote := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			out.WriteString("<blockquote>" + renderMarkdownInline(quote) + "</blockquote>\n")
			continue
		}
		if markdownTableSepRE.MatchString(trimmed) {
			continue
		}
		if markdownTableRE.MatchString(trimmed) {
			cells := splitMarkdownTableRow(trimmed)
			if !inTable {
				closeList()
				out.WriteString("<table><thead><tr>")
				for _, cell := range cells {
					out.WriteString("<th>" + renderMarkdownInline(cell) + "</th>")
				}
				out.WriteString("</tr></thead><tbody>")
				inTable = true
			} else {
				out.WriteString("<tr>")
				for _, cell := range cells {
					out.WriteString("<td>" + renderMarkdownInline(cell) + "</td>")
				}
				out.WriteString("</tr>\n")
			}
			continue
		}
		closeTable()
		if task := markdownTaskRE.FindStringSubmatch(line); task != nil {
			if !inList || inOrdered {
				closeList()
				out.WriteString("<ul>")
				inList = true
			}
			checked := task[1] == "x" || task[1] == "X"
			if checked {
				out.WriteString(`<li><input type="checkbox" checked disabled> ` + renderMarkdownInline(task[2]) + "</li>\n")
			} else {
				out.WriteString(`<li><input type="checkbox" disabled> ` + renderMarkdownInline(task[2]) + "</li>\n")
			}
			continue
		}
		if item := markdownUnorderedRE.FindStringSubmatch(line); item != nil {
			if !inList || inOrdered {
				closeList()
				out.WriteString("<ul>")
				inList = true
				inOrdered = false
			}
			out.WriteString("<li>" + renderMarkdownInline(item[1]) + "</li>\n")
			continue
		}
		if item := markdownOrderedRE.FindStringSubmatch(line); item != nil {
			if !inOrdered {
				closeList()
				out.WriteString("<ol>")
				inOrdered = true
				inList = false
			}
			out.WriteString("<li>" + renderMarkdownInline(item[1]) + "</li>\n")
			continue
		}
		if trimmed != "" {
			closeList()
			out.WriteString("<p>" + renderMarkdownInline(trimmed) + "</p>\n")
		}
	}
	closeList()
	closeTable()
	if inCodeFence {
		if codeLang == "mermaid" {
			out.WriteString(`<pre class="mermaid">` + "\n" + html.EscapeString(codeBuf.String()) + "</pre>\n")
		} else {
			out.WriteString("<pre><code>" + html.EscapeString(codeBuf.String()) + "</code></pre>\n")
		}
	}
	return out.String()
}

// splitMarkdownTableRow splits one table row into cells.
func splitMarkdownTableRow(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	parts := strings.Split(row, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func renderMarkdownInline(text string) string {
	text = strings.ReplaceAll(text, "\x00", "")
	var fragments []string
	text = markdownInlineTokenRE.ReplaceAllStringFunc(text, func(match string) string {
		parts := markdownInlineTokenRE.FindStringSubmatch(match)
		var fragment string
		if code := parts[1]; code != "" {
			fragment = "<code>" + html.EscapeString(code) + "</code>"
		} else {
			image, label, target := parts[2] == "!", parts[3], parts[4]
			if markdownTargetAllowed(target, image) {
				if image {
					fragment = `<img src="` + html.EscapeString(target) + `" alt="` + html.EscapeString(label) + `">`
				} else {
					fragment = `<a href="` + html.EscapeString(target) + `">` + renderMarkdownInline(label) + `</a>`
				}
			} else {
				fragment = html.EscapeString(label)
			}
		}
		token := fmt.Sprintf("\x00%d\x00", len(fragments)/2)
		fragments = append(fragments, token, fragment)
		return token
	})
	escaped := html.EscapeString(text)
	for _, line := range []struct {
		pattern *regexp.Regexp
		open    string
		close   string
	}{
		{regexp.MustCompile(`\*\*([^*]+)\*\*`), "<strong>", "</strong>"},
		{regexp.MustCompile(`__([^_]+)__`), "<strong>", "</strong>"},
		{regexp.MustCompile(`\*([^*]+)\*`), "<em>", "</em>"},
		{regexp.MustCompile(`_([^_]+)_`), "<em>", "</em>"},
	} {
		escaped = line.pattern.ReplaceAllString(escaped, line.open+"$1"+line.close)
	}
	return strings.NewReplacer(fragments...).Replace(escaped)
}

func markdownTargetAllowed(target string, image bool) bool {
	parsed, err := url.Parse(target)
	if err != nil || strings.Contains(target, "\\") {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "", "http", "https":
		return true
	case "mailto":
		return !image
	default:
		return false
	}
}
