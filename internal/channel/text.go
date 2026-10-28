package channel

import (
	"os"
	"path/filepath"
	"strings"
	"time"
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

// IsImageExtension reports whether name ends with a standard raster image extension.
func IsImageExtension(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

// IsPDFVisionModel reports whether modelID supports direct PDF document vision.
func IsPDFVisionModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	return strings.Contains(lower, "gemini") ||
		strings.Contains(lower, "antigravity") ||
		strings.Contains(lower, "claude")
}

// SplitCommand parses "/name argument" or "name argument", tolerating the
// "@botname" suffix messaging platforms add to group commands.
func SplitCommand(text string) (string, string) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(text), "/")
	command := trimmed
	rest := ""
	if index := strings.IndexAny(trimmed, " \n\t"); index >= 0 {
		command = trimmed[:index]
		rest = strings.TrimSpace(trimmed[index+1:])
	}
	if index := strings.Index(command, "@"); index > 0 {
		command = command[:index]
	}
	return strings.ToLower(command), rest
}

// ExtractCommandText checks whether text is a slash command, even when preceded
// by leading "@mention" tokens in a group chat (e.g. "@628123 /status").
func ExtractCommandText(text string) (string, bool) {
	rest := strings.TrimSpace(text)
	for strings.HasPrefix(rest, "@") {
		idx := strings.IndexAny(rest, " \t\n")
		if idx < 0 {
			return "", false
		}
		rest = strings.TrimSpace(rest[idx+1:])
	}
	if !strings.HasPrefix(rest, "/") || len(rest) < 2 {
		return "", false
	}
	// A slash followed by whitespace or another slash is not a command.
	second := rest[1]
	if second == ' ' || second == '\t' || second == '\n' || second == '/' {
		return "", false
	}
	return rest, true
}

// FormatQuotedPrompt prepends quoted reply context to prompt when non-empty.
func FormatQuotedPrompt(quotedText, prompt string) string {
	quoted := strings.TrimSpace(quotedText)
	if quoted == "" {
		return prompt
	}
	if len(quoted) > 2000 {
		quoted, _ = cutRunes(quoted, 2000)
		quoted += "..."
	}
	return "[Quoted message context]:\n" + quoted + "\n\n" + prompt
}

// DefaultUploadRetention is the maximum age of temporary uploaded media in .termixgo/uploads.
const DefaultUploadRetention = 7 * 24 * time.Hour

// CleanupOldUploads removes files in uploadDir older than maxAge.
func CleanupOldUploads(uploadDir string, maxAge time.Duration) {
	if maxAge <= 0 {
		maxAge = DefaultUploadRetention
	}
	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(uploadDir, entry.Name()))
		}
	}
}

// SaveUpload writes data into <workspace>/.termixgo/uploads/<fileName> and
// prunes stale files older than DefaultUploadRetention.
func SaveUpload(workspace, rawName string, data []byte) (fileName, relPath string, saved bool) {
	fileName = filepath.Base(strings.TrimSpace(rawName))
	if fileName == "." || fileName == "/" || fileName == "" {
		fileName = "uploaded_document"
	}
	relPath = filepath.ToSlash(filepath.Join(".termixgo", "uploads", fileName))
	if strings.TrimSpace(workspace) == "" {
		return fileName, relPath, false
	}
	uploadDir := filepath.Join(workspace, ".termixgo", "uploads")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return fileName, relPath, false
	}
	CleanupOldUploads(uploadDir, DefaultUploadRetention)
	fullPath := filepath.Join(uploadDir, fileName)
	if err := os.WriteFile(fullPath, data, 0o644); err != nil {
		return fileName, relPath, false
	}
	return fileName, relPath, true
}
