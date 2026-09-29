package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/99apps-id/termixgo/internal/agent"
)

// blockKind discriminates the transcript entries.
type blockKind int

const (
	blockWelcome blockKind = iota
	blockUser
	blockAssistant
	blockThinking
	blockTool
	blockPlan
	blockNotice
	blockError
)

// block is one rendered transcript entry.
type block struct {
	kind blockKind

	text      string
	reasoning string
	seconds   int64
	running   bool

	toolName   string
	toolLabel  string
	toolArgs   string
	toolResult string
	toolOK     bool
	toolMillis int64

	plan []agent.Todo
}

// transcript renders a sequence of blocks at a width. When showDetails is
// false the thinking and tool blocks are collapsed to a single status line,
// keeping the transcript focused on the conversation.
func transcript(blocks []block, styles Styles, width int, showDetails bool) string {
	if width < 24 {
		width = 24
	}
	parts := make([]string, 0, len(blocks))
	for _, item := range blocks {
		rendered := renderBlock(item, styles, width, showDetails)
		if strings.TrimSpace(rendered) == "" {
			continue
		}
		parts = append(parts, rendered)
	}
	// A blank line between blocks keeps the transcript readable.
	return strings.Join(parts, "\n\n")
}

func renderBlock(item block, styles Styles, width int, showDetails bool) string {
	switch item.kind {
	case blockWelcome:
		return renderWelcomeBlock(item, styles, width)
	case blockUser:
		return renderUserBlock(item, styles, width)
	case blockAssistant:
		return renderAssistantBlock(item, styles, width)
	case blockThinking:
		return renderThinkingBlock(item, styles, width, showDetails)
	case blockTool:
		return renderToolBlock(item, styles, width, showDetails)
	case blockPlan:
		return renderPlanBlock(item, styles, width)
	case blockNotice:
		return renderPrefixed(item.text, "  ", styles.Notice, width)
	case blockError:
		return renderPrefixed(item.text, "  ", styles.Error, width)
	default:
		return item.text
	}
}

func renderWelcomeBlock(item block, styles Styles, width int) string {
	var builder strings.Builder
	builder.WriteString(RenderBanner(styles))
	builder.WriteString("\n")
	builder.WriteString(styles.Subtitle.Render(item.text))
	if item.toolLabel != "" {
		builder.WriteString("\n")
		builder.WriteString(item.toolLabel)
	}
	return builder.String()
}

func renderUserBlock(item block, styles Styles, width int) string {
	prompt := styles.Prompt.Render("> ")
	wrapped := wrapPlain(item.text, width-4)
	lines := strings.Split(wrapped, "\n")
	for index, line := range lines {
		if index == 0 {
			lines[index] = prompt + styles.User.Render(line)
			continue
		}
		lines[index] = "  " + styles.User.Render(line)
	}
	return strings.Join(lines, "\n")
}

func renderAssistantBlock(item block, styles Styles, width int) string {
	body := renderMarkdown(item.text, styles, width-2)
	if body == "" {
		return ""
	}
	return indent(body, "  ")
}

func renderThinkingBlock(item block, styles Styles, width int, showDetails bool) string {
	header := ""
	switch {
	case item.running:
		header = styles.Thinking.Render("  Thinking...")
	case item.seconds > 0:
		header = styles.Reasoned.Render(fmt.Sprintf("  Reasoned for %ds", item.seconds))
	default:
		header = styles.Reasoned.Render("  Reasoned")
	}
	if !showDetails {
		return header
	}
	body := strings.TrimSpace(item.reasoning)
	if body == "" {
		return header
	}
	wrapped := wrapPlain(body, width-4)
	lines := strings.Split(wrapped, "\n")
	const maxLines = 8
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], styles.Reasoned.Render("  ... (reasoning trimmed)"))
	}
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		rendered = append(rendered, styles.Thinking.Render("  "+line))
	}
	return header + "\n" + strings.Join(rendered, "\n")
}

func renderToolBlock(item block, styles Styles, width int, showDetails bool) string {
	marker := "+"
	style := styles.ToolDone
	if item.running {
		marker = ">"
		style = styles.Tool
	} else if !item.toolOK {
		marker = "x"
		style = styles.ToolError
	}
	if !showDetails {
		// Compact: just the marker and tool label on one line.
		return style.Render(truncate(fmt.Sprintf("  %s %s", marker, item.toolLabel), width))
	}
	// Clip the label before styling: truncate counts runes, so slicing the
	// styled line here used to cut the timing suffix mid-escape and print
	// garbage. The suffix is computed first and kept whole.
	timing := ""
	if !item.running && item.toolMillis > 0 {
		timing = fmt.Sprintf(" (%dms)", item.toolMillis)
	}
	room := width - lipgloss.Width(timing)
	if room < 0 {
		room = 0
	}
	return style.Render(truncate(fmt.Sprintf("  %s %s", marker, item.toolLabel), room) + styles.Dim.Render(timing))
}

func renderPlanBlock(item block, styles Styles, width int) string {
	if len(item.plan) == 0 {
		return styles.Dim.Render("  Plan is empty.")
	}
	var builder strings.Builder
	builder.WriteString(styles.Plan.Render(fmt.Sprintf("  Plan (%d/%d)", countDone(item.plan), len(item.plan))))
	for _, todo := range item.plan {
		mark := "[ ]"
		style := styles.Dim
		switch todo.Status {
		case "in_progress":
			mark = "[>]"
			style = styles.Plan
		case "completed":
			mark = "[x]"
			style = styles.ToolDone
		}
		builder.WriteString("\n")
		builder.WriteString(style.Render(truncate("    "+mark+" "+todo.Title, width)))
	}
	return builder.String()
}

func countDone(items []agent.Todo) int {
	count := 0
	for _, item := range items {
		if item.Status == "completed" {
			count++
		}
	}
	return count
}

func renderPrefixed(text, prefix string, style lipgloss.Style, width int) string {
	wrapped := wrapPlain(strings.TrimSpace(text), width-len(prefix))
	if strings.TrimSpace(wrapped) == "" {
		return ""
	}
	lines := strings.Split(wrapped, "\n")
	for index := range lines {
		lines[index] = style.Render(prefix + lines[index])
	}
	return strings.Join(lines, "\n")
}

func indent(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines[index] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// renderMarkdown styles a small, deliberate subset of Markdown: headings,
// bullets, fenced code, inline code and bold. Anything else passes through.
// A full Markdown engine is not worth the dependency: the agent answers in
// plain prose with the occasional list or code block.
func renderMarkdown(text string, styles Styles, width int) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var out []string
	inCode := false
	for _, raw := range strings.Split(text, "\n") {
		trimmed := strings.TrimRight(raw, " \t")
		if strings.HasPrefix(strings.TrimSpace(trimmed), "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			out = append(out, styles.Code.Render("  "+truncate(trimmed, max(1, width-4))))
			continue
		}
		if trimmed == "" {
			out = append(out, "")
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			out = append(out, styles.Heading.Render(truncate(heading, width)))
			continue
		}
		if bullet, rest, ok := splitBullet(trimmed); ok {
			wrapped := wrapPlain(rest, max(1, width-4))
			lines := strings.Split(wrapped, "\n")
			for index, line := range lines {
				if index == 0 {
					out = append(out, styles.Bullet.Render(bullet)+" "+styleInline(line, styles))
					continue
				}
				out = append(out, "  "+styleInline(line, styles))
			}
			continue
		}
		wrapped := wrapPlain(trimmed, width)
		for _, line := range strings.Split(wrapped, "\n") {
			out = append(out, styleInline(line, styles))
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// splitBullet recognises "- ", "* " and "1. " list markers.
func splitBullet(line string) (string, string, bool) {
	for _, marker := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, marker) {
			return "*", strings.TrimSpace(line[len(marker):]), true
		}
	}
	digits := 0
	for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' && digits < 3 {
		digits++
	}
	if digits > 0 && digits+1 < len(line) && line[digits] == '.' && line[digits+1] == ' ' {
		return line[:digits+1], strings.TrimSpace(line[digits+2:]), true
	}
	return "", "", false
}

// styleInline applies `code` and **bold** inside one already-wrapped line.
func styleInline(line string, styles Styles) string {
	if line == "" {
		return ""
	}
	var builder strings.Builder
	for index := 0; index < len(line); {
		if strings.HasPrefix(line[index:], "`") {
			if end := strings.Index(line[index+1:], "`"); end >= 0 {
				builder.WriteString(styles.Code.Render(line[index+1 : index+1+end]))
				index += end + 2
				continue
			}
		}
		if strings.HasPrefix(line[index:], "**") {
			if end := strings.Index(line[index+2:], "**"); end >= 0 {
				builder.WriteString(styles.Heading.Render(line[index+2 : index+2+end]))
				index += end + 4
				continue
			}
		}
		builder.WriteByte(line[index])
		index++
	}
	return builder.String()
}

// wrapPlain word-wraps text, preserving explicit newlines and never splitting
// a word unless it is longer than the line.
func wrapPlain(text string, width int) string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		current := ""
		for _, word := range words {
			switch {
			case current == "":
				current = word
			case len([]rune(current))+1+len([]rune(word)) <= width:
				current += " " + word
			default:
				out = append(out, current)
				current = word
			}
		}
		if current != "" {
			out = append(out, current)
		}
	}
	return strings.Join(out, "\n")
}

// truncate clips a styled string to a display width.
func truncate(text string, width int) string {
	if width <= 0 || lipgloss.Width(text) <= width {
		return text
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
