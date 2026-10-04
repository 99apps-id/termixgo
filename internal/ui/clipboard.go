package ui

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// osc52MaxBytes caps the clipboard payload. Terminals limit the length of an
// OSC 52 sequence (commonly about 100 KB), so a very long transcript is
// truncated at a rune boundary instead of sent as a sequence the terminal drops.
const osc52MaxBytes = 74000

// writeClipboard copies text to the clipboard. It sends an OSC 52 escape, which
// needs no dependency and works over SSH, and also writes to the host clipboard
// through the platform's tool when one is present. An embedded webview terminal
// ignores OSC 52, so the second path is what makes a copy land there.
//
// The write is a Bubble Tea command so it runs on the update loop and never
// interleaves with a frame mid-render.
func writeClipboard(text string) tea.Cmd {
	return func() tea.Msg {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		fmt.Fprint(os.Stdout, osc52(text))
		copyToOSClipboard(text)
		return nil
	}
}

// clipboardCommand is one platform clipboard tool and its argument vector.
type clipboardCommand struct {
	name string
	args []string
}

// clipboardCommands lists the tools to try, in order, for the current OS.
func clipboardCommands() []clipboardCommand {
	switch runtime.GOOS {
	case "windows":
		return []clipboardCommand{{name: "clip"}}
	case "darwin":
		return []clipboardCommand{{name: "pbcopy"}}
	default:
		return []clipboardCommand{
			{name: "wl-copy"},
			{name: "xclip", args: []string{"-selection", "clipboard"}},
			{name: "xsel", args: []string{"--clipboard", "--input"}},
		}
	}
}

// copyToOSClipboard writes text to the host clipboard through the first
// available tool. A missing tool or a failed run is not an error: the OSC 52
// escape is the fallback.
func copyToOSClipboard(text string) {
	for _, candidate := range clipboardCommands() {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		command := exec.Command(path, candidate.args...)
		command.Stdin = strings.NewReader(text)
		if command.Run() == nil {
			return
		}
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
