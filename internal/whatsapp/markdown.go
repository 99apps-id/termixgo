package whatsapp

import (
	"regexp"
	"strings"
)

var (
	// boldItalicPattern matches ***text***
	boldItalicPattern = regexp.MustCompile(`\*\*\*([^*\n]+)\*\*\*`)
	// boldPattern matches **text**
	boldPattern = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	// underscoreBoldPattern matches __text__
	underscoreBoldPattern = regexp.MustCompile(`__([^_\n]+)__`)
	// italicAsteriskPattern matches single-asterisk *italic* without matching spaced multiplication
	italicAsteriskPattern = regexp.MustCompile(`\*([^\s*](?:[^*\n]*?[^\s*])?)\*`)
	// strikethroughPattern matches ~~text~~
	strikethroughPattern = regexp.MustCompile(`~~([^~\n]+)~~`)
	// markdownLinkPattern matches [text](url) -> text (url)
	markdownLinkPattern = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
)

// formatWhatsApp enriches markdown output for WhatsApp chat display:
//   - Headings (#, ##, ###) are converted to WhatsApp bold (*Heading*)
//   - Bold (**, __) is converted to WhatsApp single asterisk (*bold*) without double **
//   - Bold italic (***text***) is converted to WhatsApp _*text*_
//   - Strikethrough (~~text~~) is converted to WhatsApp ~text~
//   - Markdown links ([text](url)) are converted to readable "text (url)" or "text: url"
//   - Bullet items (- item, * item) are converted to clean bullet dots (• item)
//   - Fenced code blocks and inline code spans are preserved without alterations
//   - Existing emojis and formatting accents are preserved
func formatWhatsApp(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}

	lines := strings.Split(text, "\n")
	var out strings.Builder
	inCode := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check fenced code block toggles
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			out.WriteString("```")
			if i < len(lines)-1 {
				out.WriteString("\n")
			}
			continue
		}

		if inCode {
			out.WriteString(line)
			if i < len(lines)-1 {
				out.WriteString("\n")
			}
			continue
		}

		// Convert markdown horizontal rules
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			out.WriteString("──────────")
			if i < len(lines)-1 {
				out.WriteString("\n")
			}
			continue
		}

		// Convert markdown headings: # Title, ## Title, ### Title -> *Title*
		if strings.HasPrefix(trimmed, "#") {
			heading := strings.TrimLeft(trimmed, "#")
			heading = strings.TrimSpace(heading)
			if heading != "" {
				line = "*" + formatWhatsAppInline(heading) + "*"
				out.WriteString(line)
				if i < len(lines)-1 {
					out.WriteString("\n")
				}
				continue
			}
		}

		// Convert list bullets (- item or * item) to clean bullet dots (• item)
		leadingSpaces := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		trimmedLeading := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmedLeading, "- ") {
			itemContent := trimmedLeading[2:]
			line = leadingSpaces + "• " + formatWhatsAppInline(itemContent)
		} else if strings.HasPrefix(trimmedLeading, "* ") && !strings.HasPrefix(trimmedLeading, "* *") {
			itemContent := trimmedLeading[2:]
			line = leadingSpaces + "• " + formatWhatsAppInline(itemContent)
		} else {
			line = formatWhatsAppInline(line)
		}

		out.WriteString(line)
		if i < len(lines)-1 {
			out.WriteString("\n")
		}
	}

	return out.String()
}

// formatWhatsAppInline handles formatting outside inline code spans.
func formatWhatsAppInline(line string) string {
	if !strings.Contains(line, "`") {
		return applyWhatsAppInlineRules(line)
	}

	parts := strings.Split(line, "`")
	for i := 0; i < len(parts); i += 2 {
		parts[i] = applyWhatsAppInlineRules(parts[i])
	}
	return strings.Join(parts, "`")
}

func applyWhatsAppInlineRules(s string) string {
	// Extract markdown links first so underscores inside URLs (such as __init__)
	// are never rewritten by the underscore-bold emphasis rule.
	if !strings.Contains(s, "](") {
		return applyWhatsAppEmphasis(s)
	}
	var out strings.Builder
	last := 0
	for _, loc := range markdownLinkPattern.FindAllStringSubmatchIndex(s, -1) {
		out.WriteString(applyWhatsAppEmphasis(s[last:loc[0]]))
		label := applyWhatsAppEmphasis(s[loc[2]:loc[3]])
		href := s[loc[4]:loc[5]]
		out.WriteString(label + " (" + href + ")")
		last = loc[1]
	}
	out.WriteString(applyWhatsAppEmphasis(s[last:]))
	return out.String()
}

func applyWhatsAppEmphasis(s string) string {
	const (
		biOpen  = "\x00BI1\x00"
		biClose = "\x00BI2\x00"
		bOpen   = "\x00B1\x00"
		bClose  = "\x00B2\x00"
	)
	// Bold italic first: ***text*** -> _*text*_
	s = boldItalicPattern.ReplaceAllString(s, biOpen+"$1"+biClose)
	// Bold: **text** -> *text*
	s = boldPattern.ReplaceAllString(s, bOpen+"$1"+bClose)
	// Double underscore bold: __text__ -> *text*
	s = underscoreBoldPattern.ReplaceAllString(s, bOpen+"$1"+bClose)
	// Single-asterisk italic in standard Markdown: *text* -> _text_
	s = italicAsteriskPattern.ReplaceAllString(s, "_${1}_")
	// Strikethrough: ~~text~~ -> ~text~
	s = strikethroughPattern.ReplaceAllString(s, "~$1~")
	// Clean up any remaining double asterisks before restoring WhatsApp bold markers.
	s = strings.ReplaceAll(s, "**", "*")
	return strings.NewReplacer(
		biOpen, "_*",
		biClose, "*_",
		bOpen, "*",
		bClose, "*",
	).Replace(s)
}
