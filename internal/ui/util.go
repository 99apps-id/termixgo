package ui

import (
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// ansiPattern matches SGR escapes, used to strip colour for non-terminal
// output.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// Escape sequences to remove from untrusted text: a control sequence (CSI), an
// operating system command (OSC), and any other two-byte escape.
var (
	ansiCSI    = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	ansiOSC    = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	ansiEscape = regexp.MustCompile(`\x1b[^\[]`)
)

// isTerminalReader reports whether a reader is an interactive terminal.
func isTerminalReader(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

// isTerminalWriter reports whether a writer is an interactive terminal.
func isTerminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

// IsInteractive reports whether both standard streams are terminals, which is
// the condition for starting the full-screen UI.
func IsInteractive(stdin io.Reader, stdout io.Writer) bool {
	return isTerminalReader(stdin) && isTerminalWriter(stdout)
}

// stripANSI removes colour escapes.
func stripANSI(text string) string { return ansiPattern.ReplaceAllString(text, "") }

// sanitizeText removes terminal control characters from text that came from the
// model or a tool.
//
// A raw escape sequence is not prose: the terminal obeys it, so a stray cursor
// move, an unterminated SGR or a carriage return from model output rewrote the
// frame under the cursor. That is what made stored-clean text look scrambled on
// screen, with words cut and pasted from a neighbouring line. A carriage return
// is dropped (a following newline still breaks the line) and a tab becomes a
// space so the column count matches the one the wrapper measured.
func sanitizeText(text string) string {
	if strings.ContainsRune(text, 0x1b) {
		// Remove whole escape sequences first, so no parameter bytes are left
		// behind as visible text.
		text = ansiCSI.ReplaceAllString(text, "")
		text = ansiOSC.ReplaceAllString(text, "")
		text = ansiEscape.ReplaceAllString(text, "")
	}
	if !hasControl(text) {
		return text
	}
	var builder strings.Builder
	builder.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\n':
			builder.WriteRune(r)
		case r == '\t':
			builder.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// C0 controls and DEL, which include ESC and carriage return.
		case r >= 0x80 && r <= 0x9f:
			// C1 controls.
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// hasControl reports whether text holds a control character worth rewriting.
func hasControl(text string) bool {
	for _, r := range text {
		if r == '\t' || (r < 0x20 && r != '\n') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}

// trimLastRune drops the final rune, which is what a backspace means in text the
// operator typed.
//
// Cutting one byte instead would land inside the last character whenever the
// filter ends in a multi-byte one, so a filter of "\u65e5\u672c" became invalid
// UTF-8: the field painted a replacement glyph and matched nothing.
func trimLastRune(text string) string {
	if text == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(text)
	if size <= 0 {
		return ""
	}
	return text[:len(text)-size]
}

// clipBytes returns the first limit bytes of text, moved back to a rune
// boundary so the kept part is valid UTF-8.
func clipBytes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}
