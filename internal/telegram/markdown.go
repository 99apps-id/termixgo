package telegram

import (
	"regexp"
	"strings"
)

var (
	codeSpanPattern = regexp.MustCompile("`([^`\n]+)`")
	boldPattern     = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	linkPattern     = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
)

// markdownToTelegramHTML renders a small, deliberate subset of Markdown as the
// HTML that Telegram accepts: fenced code, inline code, bold, links and
// headings. Everything else passes through escaped, so a stray '<' or '&' in
// model output cannot break the message.
func markdownToTelegramHTML(text string) string {
	lines := strings.Split(text, "\n")
	var out strings.Builder
	inCode := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inCode {
				out.WriteString("</pre>")
				inCode = false
			} else {
				out.WriteString("<pre>")
				inCode = true
			}
			out.WriteString("\n")
			continue
		}
		if inCode {
			out.WriteString(escapeHTML(line))
			out.WriteString("\n")
			continue
		}
		out.WriteString(inlineToHTML(line))
		out.WriteString("\n")
	}
	if inCode {
		out.WriteString("</pre>")
	}
	return strings.TrimRight(out.String(), "\n")
}

// inlineToHTML styles one non-code line: headings, inline code, bold and links.
func inlineToHTML(line string) string {
	escaped := escapeHTML(line)
	trimmed := strings.TrimLeft(escaped, " \t")
	if strings.HasPrefix(trimmed, "#") {
		if heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#")); heading != "" {
			return "<b>" + heading + "</b>"
		}
	}
	// Inline code first, so markers inside it are not read as emphasis.
	escaped = codeSpanPattern.ReplaceAllString(escaped, "<code>$1</code>")
	escaped = boldPattern.ReplaceAllString(escaped, "<b>$1</b>")
	escaped = linkPattern.ReplaceAllString(escaped, `<a href="$2">$1</a>`)
	return escaped
}

// escapeHTML escapes the three characters Telegram's HTML parser treats as
// markup. Quotes do not need escaping in text.
func escapeHTML(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
}
