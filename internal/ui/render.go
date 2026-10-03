package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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
	blockDiff
)

// block is one rendered transcript entry.
type block struct {
	kind blockKind

	text      string
	reasoning string
	seconds   int64
	running   bool
	// finalized marks a thinking block whose closing EventReasoned has already
	// landed. The closing event arrives after the answer text has started, so
	// it can no longer find the block by looking at the tail of the transcript.
	finalized bool

	toolName   string
	toolLabel  string
	toolArgs   string
	toolResult string
	toolOK     bool
	toolMillis int64

	diffFiles  []diffFile
	diffLayout string
	diffBudget int

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
		// Model and tool text can carry terminal control characters. They are
		// dropped before any styling so the terminal never obeys a sequence the
		// agent happened to print, which is what scrambled the frame.
		item.text = sanitizeText(item.text)
		item.reasoning = sanitizeText(item.reasoning)
		item.toolLabel = sanitizeText(item.toolLabel)
		item.toolResult = sanitizeText(item.toolResult)
		for index := range item.plan {
			item.plan[index].Title = sanitizeText(item.plan[index].Title)
		}
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
	case blockDiff:
		if len(item.diffFiles) == 0 {
			return ""
		}
		// The diff is drawn from the parsed files on every frame, not cached at
		// the width it was opened with. A cached body kept its old columns after
		// a resize, so narrowing the terminal clipped the new side away instead
		// of falling back to one column.
		if item.diffLayout == "side" && width < 100 {
			return renderDiffUnified(item.diffFiles, styles, width, item.diffBudget)
		}
		if item.diffLayout == "side" {
			return renderDiffSideBySide(item.diffFiles, styles, width, item.diffBudget)
		}
		return renderDiffUnified(item.diffFiles, styles, width, item.diffBudget)
	default:
		return item.text
	}
}

func renderWelcomeBlock(item block, styles Styles, width int) string {
	if width < 8 {
		width = 8
	}
	var lines []string
	// The banner is fixed art, so it is clipped rather than wrapped. Without
	// this the block keeps its own width at a narrow terminal, and a line wider
	// than the terminal wraps and pushes the rest of the frame down.
	for _, row := range strings.Split(RenderBanner(styles), "\n") {
		lines = append(lines, truncate(row, width))
	}
	lines = append(lines, "")
	// The info text wraps to the terminal so nothing is lost, and each line is
	// rendered on its own so a style that pads cannot widen the block.
	for _, paragraph := range strings.Split(item.text, "\n") {
		wrapped := wrapPlain(paragraph, width)
		if strings.TrimSpace(wrapped) == "" {
			lines = append(lines, "")
			continue
		}
		for _, line := range strings.Split(wrapped, "\n") {
			lines = append(lines, truncate(styles.Subtitle.Render(line), width))
		}
	}
	if item.toolLabel != "" {
		lines = append(lines, truncate(item.toolLabel, width))
	}
	return strings.Join(lines, "\n")
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
		header = shimmerLine("  Thinking...", styles)
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
		line := truncate(fmt.Sprintf("  %s %s", marker, item.toolLabel), width)
		if item.running {
			return shimmerTool(line, styles)
		}
		return style.Render(line)
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
	line := truncate(fmt.Sprintf("  %s %s", marker, item.toolLabel), room)
	if item.running {
		return shimmerTool(line, styles)
	}
	rendered := style.Render(line + styles.Dim.Render(timing))
	result := strings.TrimSpace(item.toolResult)
	if result == "" {
		return rendered
	}
	display := clipBytes(result, markdownResultCap)
	if len(result) > markdownResultCap {
		display += "\n... [tool output clipped]"
	}
	return rendered + "\n" + renderPrefixed(display, "    ", styles.Dim, width)
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
		lines[index] = prefix + strings.TrimRight(line, " \t\r")
	}
	return strings.Join(lines, "\n")
}

// markdownResultCap is the maximum bytes a tool result block may display in
// the TUI. The transcript must stay inside the terminal box, and a command
// output of tens of thousands of bytes would push the frame past it.
const markdownResultCap = 12000

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
		trimmed := strings.TrimRight(raw, " \t\r")
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
			wrapped := wrapInline(rest, max(1, width-4))
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
		wrapped := wrapInline(trimmed, width)
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
// Code spans win over bold: a ** pair spanning backticks still renders the
// code inside instead of printing the markers literally.
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
				builder.WriteString(styles.Heading.Render(styleInlineCode(line[index+2:index+2+end], styles)))
				index += end + 4
				continue
			}
		}
		builder.WriteByte(line[index])
		index++
	}
	return builder.String()
}

// styleInlineCode renders only `code` spans, for text already inside bold.
func styleInlineCode(text string, styles Styles) string {
	var builder strings.Builder
	for index := 0; index < len(text); {
		if text[index] == '`' {
			if end := strings.Index(text[index+1:], "`"); end >= 0 {
				builder.WriteString(styles.Code.Render(text[index+1 : index+1+end]))
				index += end + 2
				continue
			}
		}
		builder.WriteByte(text[index])
		index++
	}
	return builder.String()
}

// wrapInline wraps a line that carries inline spans, keeping every `code` and
// **bold** span whole.
//
// wrapPlain breaks at any space, including a space inside a span. A span split
// across two lines then has no closing marker on the first line and no opening
// one on the second, so styleInline cannot pair it and leaves the markers
// literal: the operator sees a stray backtick at the end of one line and the
// matching one at the start of the next. Spans are why this exists, so a whole
// span is one unbreakable word here.
func wrapInline(text string, width int) string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		words := splitInlineWords(paragraph)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		current := ""
		currentWidth := 0
		for _, word := range words {
			// A word wider than the line is broken into tokens. A token marked
			// glued continues the previous one with no space, which is how a
			// long word or span is split without turning into two words.
			for _, token := range splitInlineWord(word, width) {
				tokenWidth := inlineWidth(token.text)
				switch {
				case current == "":
					current = token.text
					currentWidth = tokenWidth
				case token.glue && currentWidth+tokenWidth <= width:
					current += token.text
					currentWidth += tokenWidth
				case !token.glue && currentWidth+1+tokenWidth <= width:
					current += " " + token.text
					currentWidth += 1 + tokenWidth
				default:
					out = append(out, current)
					current = token.text
					currentWidth = tokenWidth
				}
			}
		}
		if current != "" {
			out = append(out, current)
		}
	}
	return strings.Join(out, "\n")
}

// inlineToken is one piece of a word. glue means the piece continues the
// previous one with no space, which is what a hard-split word or span needs.
type inlineToken struct {
	text string
	glue bool
}

// splitInlineWord turns one word into tokens that each fit the width. A short
// word is one token. A long word is hard-split, its later pieces glued. A span
// wider than the line keeps its markers on every piece, so no line has an
// unpaired marker and no line exceeds the terminal.
func splitInlineWord(word string, width int) []inlineToken {
	if inlineWidth(word) <= width {
		return []inlineToken{{text: word}}
	}
	inner, ok := spanInner(word)
	if !ok {
		chunks := hardSplit(word, width)
		tokens := make([]inlineToken, 0, len(chunks))
		for index, chunk := range chunks {
			tokens = append(tokens, inlineToken{text: chunk, glue: index > 0})
		}
		return tokens
	}
	marker := "`"
	if strings.HasPrefix(word, "**") {
		marker = "**"
	}
	room := width - 2*len(marker)
	if room < 8 {
		room = 8
	}
	tokens := make([]inlineToken, 0, 4)
	var line strings.Builder
	lineWidth := 0
	flush := func() {
		if line.Len() == 0 {
			return
		}
		tokens = append(tokens, inlineToken{text: marker + line.String() + marker, glue: len(tokens) > 0})
		line.Reset()
		lineWidth = 0
	}
	for _, piece := range splitInlineWords(inner) {
		for index, chunk := range hardSplit(piece, room) {
			chunkWidth := inlineWidth(chunk)
			switch {
			case lineWidth == 0:
				line.WriteString(chunk)
				lineWidth = chunkWidth
			case index == 0 && lineWidth+1+chunkWidth <= room:
				line.WriteByte(' ')
				line.WriteString(chunk)
				lineWidth += 1 + chunkWidth
			default:
				flush()
				line.WriteString(chunk)
				lineWidth = chunkWidth
			}
		}
	}
	flush()
	return tokens
}

// splitInlineWords cuts a paragraph into words where the spaces inside a
// complete span do not separate. Punctuation next to a span stays attached to
// it, which is what keeps "(`src-tauri/`)" one token.
func splitInlineWords(text string) []string {
	var words []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	for index := 0; index < len(text); {
		switch text[index] {
		case ' ', '\t', '\r':
			flush()
			index++
			continue
		}
		if span := completeSpan(text[index:]); span != "" {
			current.WriteString(span)
			index += len(span)
			continue
		}
		current.WriteByte(text[index])
		index++
	}
	flush()
	return words
}

// completeSpan returns the whole span at the start of text, markers included,
// or "" when no closed span begins there. An unclosed marker is deliberately
// not a span, which is what keeps a lone ` or ** rendering as literal text.
func completeSpan(text string) string {
	switch {
	case strings.HasPrefix(text, "`"):
		if end := strings.Index(text[1:], "`"); end >= 0 {
			return text[:end+2]
		}
	case strings.HasPrefix(text, "**"):
		if end := strings.Index(text[2:], "**"); end >= 0 {
			return text[:end+4]
		}
	}
	return ""
}

// spanInner reports a span's text and whether the whole string is one span.
func spanInner(text string) (string, bool) {
	for _, marker := range []string{"`", "**"} {
		if strings.HasPrefix(text, marker) && strings.HasSuffix(text, marker) && len(text) >= 2*len(marker) {
			return text[len(marker) : len(text)-len(marker)], true
		}
	}
	return "", false
}

// inlineWidth counts the columns a word occupies once styled, so the markers
// a span carries do not count against the line budget.
//
// The count is display columns, not runes: a wide glyph such as a CJK
// character or an emoji occupies two cells, and wrapping by rune count let a
// line measure short, overflow the terminal and wrap, which is what shifted
// the whole frame. ansi.StringWidth is the same measurement truncate uses.
func inlineWidth(word string) int {
	if inner, ok := spanInner(word); ok {
		return ansi.StringWidth(inner)
	}
	return ansi.StringWidth(word)
}

// wrapPlain word-wraps text, preserving explicit newlines. A word wider than
// the line is hard-split at the column budget, because an over-wide line wraps
// in the terminal and shifts every row below it, which is what made the frame
// look scrambled.
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
		currentWidth := 0
		for _, word := range words {
			// A long word is broken into fitting pieces; the pieces after the
			// first continue the word, so they start a line with no space.
			for index, piece := range hardSplit(word, width) {
				pieceWidth := ansi.StringWidth(piece)
				switch {
				case current == "":
					current = piece
					currentWidth = pieceWidth
				case index == 0 && currentWidth+1+pieceWidth <= width:
					current += " " + piece
					currentWidth += 1 + pieceWidth
				default:
					out = append(out, current)
					current = piece
					currentWidth = pieceWidth
				}
			}
		}
		if current != "" {
			out = append(out, current)
		}
	}
	return strings.Join(out, "\n")
}

// hardSplit breaks a string that is wider than the budget into pieces that each
// fit, measured in display columns and never through a rune. It is the last line
// of defence against a word with no space: a URL, a hash or a long identifier
// would otherwise produce a line the terminal wraps on its own.
func hardSplit(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	if ansi.StringWidth(text) <= width {
		return []string{text}
	}
	pieces := make([]string, 0, 2)
	var builder strings.Builder
	current := 0
	for _, r := range text {
		runeWidth := ansi.StringWidth(string(r))
		if current+runeWidth > width && builder.Len() > 0 {
			pieces = append(pieces, builder.String())
			builder.Reset()
			current = 0
		}
		builder.WriteRune(r)
		current += runeWidth
	}
	if builder.Len() > 0 {
		pieces = append(pieces, builder.String())
	}
	return pieces
}

// truncate clips a string to a display width, preserving colour.
//
// It must be ANSI-aware: some callers hand it a string that is already styled,
// and slicing one by rune cuts through an escape sequence. A terminal that meets
// a broken sequence swallows the characters after it looking for the end, which
// is exactly what makes the screen look scrambled even though the stored text is
// intact. The tail is three dots to keep the historical output shape.
func truncate(text string, width int) string {
	// A non-positive width means "no limit" to the callers that pass one
	// through unchecked, so the text is returned as-is rather than dropped.
	if width <= 0 || ansi.StringWidth(text) <= width {
		return text
	}
	if width <= 3 {
		return ansi.Truncate(text, width, "")
	}
	// ansi.Truncate counts the tail inside the target width, which is what the
	// historical output shape ("abc...") expects.
	return ansi.Truncate(text, width, "...")
}

// truncateLeft clips a string to a display width from the start, keeping the
// tail. A path is the case it exists for: the last segments name the folder the
// operator recognises, while the drive and the user directories do not.
func truncateLeft(text string, width int) string {
	if width <= 0 {
		return ""
	}
	excess := ansi.StringWidth(text) - width
	if excess <= 0 {
		return text
	}
	if width <= 3 {
		return ansi.TruncateLeft(text, excess, "")
	}
	// The prefix takes three columns of the target width, so three more
	// characters have to come off the left than the excess alone.
	return ansi.TruncateLeft(text, excess+3, "...")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
