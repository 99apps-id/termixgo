package ui

import (
	"io"
	"os"
	"regexp"
	"unicode/utf8"

	"golang.org/x/term"
)

// ansiPattern matches SGR escapes, used to strip colour for non-terminal
// output.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

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
