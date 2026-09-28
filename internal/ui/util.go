package ui

import (
	"io"
	"os"
	"regexp"

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
