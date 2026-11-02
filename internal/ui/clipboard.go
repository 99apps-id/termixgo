package ui

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
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
// or Windows terminal without OSC 52 enabled will benefit from the host tool.
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

// clipboardReadTimeout bounds a clipboard helper. A tool that waits on a
// selection owner with no content would otherwise freeze the interface.
const clipboardReadTimeout = 5 * time.Second

// clipboardReadCommands lists the tools that read text from the host
// clipboard, in order, for the current OS. They mirror clipboardCommands,
// which is the write side.
func clipboardReadCommands() []clipboardCommand {
	switch runtime.GOOS {
	case "windows":
		return []clipboardCommand{{
			name: "powershell.exe",
			args: []string{"-NoProfile", "-NonInteractive", "-Command", "Get-Clipboard -Raw"},
		}}
	case "darwin":
		return []clipboardCommand{{name: "pbpaste"}}
	default:
		return []clipboardCommand{
			{name: "wl-paste", args: []string{"--no-newline"}},
			{name: "xclip", args: []string{"-selection", "clipboard", "-o"}},
			{name: "xsel", args: []string{"--clipboard", "--output"}},
		}
	}
}

// readClipboardText returns the host clipboard as text. The terminal's own
// paste is bracketed text and cannot carry pixels or a file, so this is how
// /paste reaches the operator's real clipboard.
func readClipboardText() (string, error) {
	var lastErr error
	for _, candidate := range clipboardReadCommands() {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), clipboardReadTimeout)
		command := exec.CommandContext(ctx, path, candidate.args...)
		output, err := command.Output()
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		return string(output), nil
	}
	if lastErr != nil {
		return "", fmt.Errorf("cannot read the clipboard: %v", lastErr)
	}
	return "", fmt.Errorf("no clipboard reader found: install wl-clipboard, xclip or xsel (pbcopy on macOS)")
}

// readClipboardImage returns PNG bytes from the host clipboard. The platform
// tools each handle one shape: pngpaste on macOS, wl-paste or xclip on Linux,
// and a PowerShell script on Windows. A machine without one is told which
// tool to install rather than left with an empty attach.
func readClipboardImage() ([]byte, error) {
	switch runtime.GOOS {
	case "windows":
		return readClipboardImageWindows()
	case "darwin":
		return runClipboardImageCommand("pngpaste", "-")
	default:
		wl, err := exec.LookPath("wl-paste")
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), clipboardReadTimeout)
			command := exec.CommandContext(ctx, wl, "--no-newline", "--type", "image/png")
			output, runErr := command.Output()
			cancel()
			if runErr == nil && len(output) > 0 {
				return output, nil
			}
		}
		xclip, err := exec.LookPath("xclip")
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), clipboardReadTimeout)
			command := exec.CommandContext(ctx, xclip, "-selection", "clipboard", "-t", "image/png", "-o")
			output, runErr := command.Output()
			cancel()
			if runErr == nil && len(output) > 0 {
				return output, nil
			}
		}
		return nil, fmt.Errorf("no clipboard image reader found: install wl-clipboard (Wayland) or xclip (X11), or pass the image to /attach")
	}
}

// readClipboardImageWindows asks the .NET clipboard for a bitmap and writes it
// to a temporary PNG, which is the only shape PowerShell can hand back
// through a pipe reliably.
func readClipboardImageWindows() ([]byte, error) {
	script := `Add-Type -AssemblyName System.Windows.Forms,System.Drawing
if (-not [System.Windows.Forms.Clipboard]::ContainsImage()) { exit 2 }
$image = [System.Windows.Forms.Clipboard]::GetImage()
$path = Join-Path $env:TEMP ("termixgo-clip-" + [guid]::NewGuid().ToString("N") + ".png")
$image.Save($path, [System.Drawing.Imaging.ImageFormat]::Png)
Write-Output $path`
	ctx, cancel := context.WithTimeout(context.Background(), clipboardReadTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("the clipboard holds no image")
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		return nil, fmt.Errorf("the clipboard holds no image")
	}
	data, readErr := os.ReadFile(path)
	_ = os.Remove(path)
	if readErr != nil {
		return nil, fmt.Errorf("cannot read the clipboard image: %v", readErr)
	}
	return data, nil
}

func runClipboardImageCommand(name string, args ...string) ([]byte, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("no clipboard image reader found: install %s, or pass the image to /attach", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), clipboardReadTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, args...)
	output, err := command.Output()
	if err != nil || len(output) == 0 {
		return nil, fmt.Errorf("the clipboard holds no image")
	}
	return output, nil
}
