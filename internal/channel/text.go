package channel

import (
	"strings"
	"unicode/utf8"

	"github.com/99apps-id/termixgo/internal/config"
)

// Silent reports whether an answer must not be delivered at all.
//
// A scheduled or background run says HEARTBEAT_OK when it has nothing to
// report, and a model sometimes narrates its own silence; neither belongs in a
// chat. A message that merely contains the word must still go out, so the
// match is anchored and length-guarded.
func Silent(text string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(text))
	if trimmed == "" {
		return true
	}
	trimmed = strings.Trim(trimmed, "*_~` ")
	trimmed = strings.TrimSuffix(trimmed, ".")
	trimmed = strings.TrimSpace(trimmed)
	if len(trimmed) > 64 {
		return false
	}
	switch trimmed {
	case "silent", "(silent)", "no response", "(no response)", "no reply", "(no reply)",
		"[silent]", "heartbeat_ok", "[heartbeat_ok]":
		return true
	}
	return false
}

// Chunk splits an answer into messages no longer than limit bytes.
//
// The split prefers paragraph boundaries, then line boundaries, and only cuts
// mid-line when a single line cannot fit. Every cut happens on a rune boundary:
// a message that ends in half a multi-byte character is rejected by the
// transport, which would lose the whole chunk.
func Chunk(text string, limit int) []string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if trimmed == "" {
		return nil
	}
	if limit <= 0 {
		limit = config.DefaultTextChunkLimit
	}

	var chunks []string
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		piece := strings.TrimRight(current.String(), "\n")
		if strings.TrimSpace(piece) != "" {
			chunks = append(chunks, piece)
		}
		current.Reset()
	}

	for _, paragraph := range strings.Split(trimmed, "\n\n") {
		if strings.TrimSpace(paragraph) == "" {
			continue
		}
		if len(paragraph) <= limit {
			if current.Len() > 0 && current.Len()+2+len(paragraph) > limit {
				flush()
			}
			if current.Len() > 0 {
				current.WriteString("\n\n")
			}
			current.WriteString(paragraph)
			continue
		}
		flush()
		chunks = append(chunks, splitParagraph(paragraph, limit)...)
	}
	flush()
	return chunks
}

// splitParagraph packs a too-long paragraph's lines into chunks, splitting a
// single over-long line on rune boundaries.
func splitParagraph(paragraph string, limit int) []string {
	var chunks []string
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		chunks = append(chunks, strings.TrimRight(current.String(), "\n"))
		current.Reset()
	}
	for _, line := range strings.Split(paragraph, "\n") {
		for len(line) > limit {
			flush()
			head, tail := cutRunes(line, limit)
			chunks = append(chunks, head)
			line = tail
		}
		if current.Len() > 0 && current.Len()+1+len(line) > limit {
			flush()
		}
		if current.Len() > 0 {
			current.WriteByte('\n')
		}
		current.WriteString(line)
	}
	flush()
	return chunks
}

// cutRunes splits text at a byte limit without cutting a rune in half.
func cutRunes(text string, limit int) (head, tail string) {
	if limit >= len(text) {
		return text, ""
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	if cut == 0 {
		// A single rune wider than the limit; keep it whole rather than
		// emitting invalid UTF-8, which the transport would reject.
		_, size := utf8.DecodeRuneInString(text)
		cut = size
	}
	return text[:cut], text[cut:]
}
