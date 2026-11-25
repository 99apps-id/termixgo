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

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/proc"
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
		proc.Detach(command)
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
			{name: "wl-paste", args: []string{"--no-newline", "--type", "text/plain"}},
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
	if runtime.GOOS == "windows" {
		if text, err := readClipboardTextNative(); err == nil {
			return text, nil
		}
	}
	var lastErr error
	for _, candidate := range clipboardReadCommands() {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), clipboardReadTimeout)
		command := exec.CommandContext(ctx, path, candidate.args...)
		proc.Detach(command)
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
// readClipboardImageOrPaths returns PNG bytes or copied image file paths from
// the host clipboard.
func readClipboardImageOrPaths() ([]byte, []string, error) {
	if runtime.GOOS == "windows" {
		data, paths, err := readClipboardImageNative()
		if err == nil && (len(data) > 0 || len(paths) > 0) {
			return data, paths, nil
		}
		psData, psErr := readClipboardImageWindows()
		if psErr == nil && len(psData) > 0 {
			return psData, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, psErr
	}
	data, err := readClipboardImage()
	return data, nil, err
}

func readClipboardImage() ([]byte, error) {
	switch runtime.GOOS {
	case "windows":
		if data, paths, err := readClipboardImageNative(); err == nil {
			if len(data) > 0 {
				return data, nil
			}
			if len(paths) > 0 {
				if raw, readErr := os.ReadFile(paths[0]); readErr == nil && len(raw) > 0 {
					return raw, nil
				}
			}
		}
		return readClipboardImageWindows()
	case "darwin":
		return runClipboardImageCommand("pngpaste", "-")
	default:
		wl, err := exec.LookPath("wl-paste")
		if err == nil {
			for _, mime := range []string{"image/png", "image/jpeg"} {
				ctx, cancel := context.WithTimeout(context.Background(), clipboardReadTimeout)
				command := exec.CommandContext(ctx, wl, "--no-newline", "--type", mime)
				output, runErr := command.Output()
				cancel()
				if runErr == nil && len(output) > 0 {
					return output, nil
				}
			}
		}
		xclip, err := exec.LookPath("xclip")
		if err == nil {
			for _, mime := range []string{"image/png", "image/jpeg"} {
				ctx, cancel := context.WithTimeout(context.Background(), clipboardReadTimeout)
				command := exec.CommandContext(ctx, xclip, "-selection", "clipboard", "-t", mime, "-o")
				output, runErr := command.Output()
				cancel()
				if runErr == nil && len(output) > 0 {
					return output, nil
				}
			}
		}
		return nil, fmt.Errorf("no clipboard image reader found: install wl-clipboard (Wayland) or xclip (X11), or pass the image to /attach")
	}
}

// readClipboardImageWindows asks the .NET clipboard for a bitmap or copied
// image file list and writes/reads a temporary PNG as a fallback when the
// native Win32 decoder cannot handle the format.
func readClipboardImageWindows() ([]byte, error) {
	script := `Add-Type -AssemblyName System.Windows.Forms,System.Drawing
if ([System.Windows.Forms.Clipboard]::ContainsImage()) {
	$image = [System.Windows.Forms.Clipboard]::GetImage()
	if ($null -ne $image) {
		$path = Join-Path $env:TEMP ("termixgo-clip-" + [guid]::NewGuid().ToString("N") + ".png")
		$image.Save($path, [System.Drawing.Imaging.ImageFormat]::Png)
		Write-Output ("FILE:" + $path)
		exit 0
	}
}
if ([System.Windows.Forms.Clipboard]::ContainsFileDropList()) {
	foreach ($f in [System.Windows.Forms.Clipboard]::GetFileDropList()) {
		if (Test-Path -LiteralPath $f -PathType Leaf) {
			Write-Output ("PATH:" + $f)
			exit 0
		}
	}
}
exit 2`
	ctx, cancel := context.WithTimeout(context.Background(), clipboardReadTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "powershell.exe", "-STA", "-NoProfile", "-NonInteractive", "-Command", script)
	proc.Detach(command)
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("the clipboard holds no image")
	}
	line := strings.TrimSpace(string(output))
	switch {
	case strings.HasPrefix(line, "FILE:"):
		path := strings.TrimSpace(strings.TrimPrefix(line, "FILE:"))
		data, readErr := os.ReadFile(path)
		_ = os.Remove(path)
		if readErr != nil {
			return nil, fmt.Errorf("cannot read the clipboard image: %v", readErr)
		}
		return data, nil
	case strings.HasPrefix(line, "PATH:"):
		path := strings.TrimSpace(strings.TrimPrefix(line, "PATH:"))
		if _, ok := agent.ImageMediaType(path); ok {
			data, readErr := os.ReadFile(path)
			if readErr == nil && len(data) > 0 {
				return data, nil
			}
		}
	}
	return nil, fmt.Errorf("the clipboard holds no image")
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
