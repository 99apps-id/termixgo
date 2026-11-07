package telegram

import (
	"net/http"
	"strings"
	"testing"
)

func TestNextChunkBalancesCodeFence(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("intro line\n\n```go\n")
	for index := 0; index < 400; index++ {
		builder.WriteString("fmt.Println(\"a fairly long line of code\")\n")
	}
	builder.WriteString("```\n")
	text := builder.String()

	part, rest := nextChunk(text, telegramChunkLimit)
	if rest == "" {
		t.Fatalf("expected a fenced answer longer than the limit to split")
	}
	if strings.Count(part, "```")%2 != 0 {
		t.Errorf("first chunk leaves an unterminated fence:\n%s", part)
	}
	if !strings.HasPrefix(strings.TrimSpace(rest), "```") {
		t.Errorf("second chunk does not reopen the fence: %q", firstLine(rest))
	}
	if html := markdownToTelegramHTML(part); strings.Contains(html, "<pre><code") && !strings.Contains(html, "</code></pre>") {
		t.Errorf("first chunk converted to unbalanced HTML:\n%s", html)
	}
}

func TestNextChunkLeavesBalancedFenceAlone(t *testing.T) {
	text := "```sh\n" + strings.Repeat("echo hello\n", 700) + "```\n"
	part, rest := nextChunk(text, telegramChunkLimit)
	if rest == "" {
		t.Fatalf("expected a long answer to split")
	}
	if strings.Count(part, "```")%2 != 0 {
		t.Errorf("first chunk leaves an unterminated fence")
	}
	if !strings.HasPrefix(strings.TrimSpace(rest), "```") {
		t.Errorf("second chunk does not reopen the fence")
	}
}

func TestCapCardLinesNamesDroppedLines(t *testing.T) {
	lines := []string{"one", "two", "three", "four", "five"}
	kept := capCardLines(lines, 12)
	if len(kept) < 2 {
		t.Fatalf("expected a marker plus kept lines, got %v", kept)
	}
	if !strings.HasPrefix(kept[0], "... (") || !strings.Contains(kept[0], "earlier lines dropped") {
		t.Errorf("first kept row = %q, want a dropped-lines marker", kept[0])
	}
	if kept[len(kept)-1] != "five" {
		t.Errorf("newest line dropped: %v", kept)
	}
}

func TestCapCardLinesKeepsShortListWhole(t *testing.T) {
	lines := []string{"one", "two"}
	kept := capCardLines(lines, 100)
	if len(kept) != 2 || kept[0] != "one" || kept[1] != "two" {
		t.Errorf("short card was modified: %v", kept)
	}
}

func TestIsParseErrorRecognisesEntityVariants(t *testing.T) {
	cases := []string{
		"can't parse entities: Unsupported start tag",
		"Bad Request: can't find end of entity",
		"Bad Request: unsupported start tag \"b\"",
		"Bad Request: can't parse entity",
	}
	for _, description := range cases {
		err := &APIError{Code: http.StatusBadRequest, Description: description}
		if !isParseError(err) {
			t.Errorf("isParseError(%q) = false, want true", description)
		}
	}
	if isParseError(&APIError{Code: http.StatusBadRequest, Description: "chat not found"}) {
		t.Errorf("isParseError accepted an unrelated 400")
	}
	if isParseError(&APIError{Code: http.StatusTooManyRequests, Description: "parse"}) {
		t.Errorf("isParseError accepted a non-400 status")
	}
}

func TestStripTelegramHTML(t *testing.T) {
	html := "<b>bold</b> &amp; <code>a &lt; b</code>"
	got := stripTelegramHTML(html)
	want := "bold & a < b"
	if got != want {
		t.Errorf("stripTelegramHTML = %q, want %q", got, want)
	}
}

func TestValidUTF8ReplacesBrokenBytes(t *testing.T) {
	broken := "ok\xff\xfeend"
	got := validUTF8(broken)
	if !strings.Contains(got, "ok") || !strings.Contains(got, "end") {
		t.Errorf("validUTF8 lost content: %q", got)
	}
	if strings.ContainsRune(got, 0xff) {
		t.Errorf("validUTF8 left an invalid byte: %q", got)
	}
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

func TestIsNotModifiedRecognisesTheEditNoop(t *testing.T) {
	err := &APIError{Code: http.StatusBadRequest, Description: "Bad Request: message is not modified"}
	if !isNotModified(err) {
		t.Errorf("isNotModified rejected the documented no-op edit")
	}
	if isNotModified(&APIError{Code: http.StatusBadRequest, Description: "chat not found"}) {
		t.Errorf("isNotModified accepted an unrelated 400")
	}
	if isNotModified(&APIError{Code: http.StatusTooManyRequests, Description: "message is not modified"}) {
		t.Errorf("isNotModified accepted a non-400 status")
	}
}
