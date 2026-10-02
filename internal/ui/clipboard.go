package ui

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// osc52MaxBytes caps the clipboard payload. Terminals limit the length of an
// OSC 52 sequence (commonly about 100 KB), so a very long transcript is
// truncated at a rune boundary instead of sent as a sequence the terminal drops.
const osc52MaxBytes = 74000

// writeClipboard copies text to the terminal's clipboard with an OSC 52 escape.
// It needs no dependency and works over SSH, unlike a native clipboard call.
// The write is a Bubble Tea command so it runs on the update loop and never
// interleaves with a frame mid-render.
func writeClipboard(text string) tea.Cmd {
	return func() tea.Msg {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		fmt.Fprint(os.Stdout, osc52(text))
		return nil
	}
}

// osc52 builds the escape sequence. OSC 52 carries the payload base64 encoded.
func osc52(text string) string {
	text = clampClipboard(text)
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
}

// clampClipboard truncates to the OSC 52 budget without splitting a rune.
func clampClipboard(text string) string {
	if len(text) <= osc52MaxBytes {
		return text
	}
	cut := osc52MaxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}
