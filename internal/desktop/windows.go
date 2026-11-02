//go:build windows

package desktop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type windowsController struct{}

func newController() Controller { return windowsController{} }

// requirements is empty on Windows: every driver here is PowerShell and the
// .NET assemblies that ship with it.
func requirements() []string { return nil }

func powershell(ctx context.Context, script string, env []string) error {
	binary, err := requireTool("powershell.exe", "pwsh.exe")
	if err != nil {
		return err
	}
	_, err = outputEnv(ctx, binary, env, "-NoProfile", "-NonInteractive", "-Command", script)
	return err
}

func powershellOutput(ctx context.Context, script string, env []string) (string, error) {
	binary, err := requireTool("powershell.exe", "pwsh.exe")
	if err != nil {
		return "", err
	}
	return outputEnv(ctx, binary, env, "-NoProfile", "-NonInteractive", "-Command", script)
}

const windowsCaptureScript = `Add-Type -AssemblyName System.Windows.Forms,System.Drawing
$output = $env:TERMIXGO_DESKTOP_OUTPUT
$x = [int]$env:TERMIXGO_DESKTOP_X
$y = [int]$env:TERMIXGO_DESKTOP_Y
$w = [int]$env:TERMIXGO_DESKTOP_W
$h = [int]$env:TERMIXGO_DESKTOP_H
if ($w -le 0 -or $h -le 0) {
  $bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
  $x = $bounds.X
  $y = $bounds.Y
  $w = $bounds.Width
  $h = $bounds.Height
}
$bitmap = New-Object System.Drawing.Bitmap $w, $h
$graphics = [System.Drawing.Graphics]::FromImage($bitmap)
$graphics.CopyFromScreen($x, $y, 0, 0, $bitmap.Size)
$bitmap.Save($output, [System.Drawing.Imaging.ImageFormat]::Png)
$graphics.Dispose()
$bitmap.Dispose()`

func (windowsController) Capture(ctx context.Context, request CaptureRequest) error {
	if err := ensureOutputPath(request.Output); err != nil {
		return err
	}
	if request.Window != "" {
		return fmt.Errorf("capturing one window is not supported on Windows: focus it with desktop_windows and capture the screen")
	}
	region := request.Region
	if region == nil {
		region = &Region{}
	}
	env := append(os.Environ(),
		"TERMIXGO_DESKTOP_OUTPUT="+request.Output,
		"TERMIXGO_DESKTOP_X="+strconv.Itoa(region.X),
		"TERMIXGO_DESKTOP_Y="+strconv.Itoa(region.Y),
		"TERMIXGO_DESKTOP_W="+strconv.Itoa(region.Width),
		"TERMIXGO_DESKTOP_H="+strconv.Itoa(region.Height),
	)
	return powershell(ctx, windowsCaptureScript, env)
}

const windowsListScript = `Get-Process | Where-Object { $_.MainWindowTitle -ne "" } | ForEach-Object { "$($_.Id)` + "\t" + `$($_.ProcessName)` + "\t" + `$($_.MainWindowTitle)" }`

func (windowsController) ListWindows(ctx context.Context) ([]Window, error) {
	raw, err := powershellOutput(ctx, windowsListScript, os.Environ())
	if err != nil {
		return nil, err
	}
	return parseWindows(raw), nil
}

const windowsFocusScript = `$shell = New-Object -ComObject WScript.Shell
$target = $env:TERMIXGO_DESKTOP_QUERY
if ($shell.AppActivate($target)) { exit 0 }
$process = Get-Process | Where-Object { $_.MainWindowTitle -like "*$target*" } | Select-Object -First 1
if ($process -and $shell.AppActivate($process.Id)) { exit 0 }
exit 3`

func (windowsController) FocusWindow(ctx context.Context, query string) error {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return fmt.Errorf("a window title or process name is required")
	}
	env := append(os.Environ(), "TERMIXGO_DESKTOP_QUERY="+trimmed)
	if err := powershell(ctx, windowsFocusScript, env); err != nil {
		return fmt.Errorf("no window matches %q", trimmed)
	}
	return nil
}

const windowsInputPrelude = `Add-Type @"
using System;
using System.Runtime.InteropServices;
public class TermixInput {
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int X, int Y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint dwFlags, uint dx, uint dy, uint dwData, UIntPtr dwExtraInfo);
}
"@
`

const windowsMoveScript = windowsInputPrelude + `[TermixInput]::SetCursorPos([int]$env:TERMIXGO_DESKTOP_X, [int]$env:TERMIXGO_DESKTOP_Y) | Out-Null`

func (windowsController) Move(ctx context.Context, point Point) error {
	env := append(os.Environ(),
		"TERMIXGO_DESKTOP_X="+strconv.Itoa(point.X),
		"TERMIXGO_DESKTOP_Y="+strconv.Itoa(point.Y),
	)
	return powershell(ctx, windowsMoveScript, env)
}

const windowsClickScript = windowsInputPrelude + `[TermixInput]::SetCursorPos([int]$env:TERMIXGO_DESKTOP_X, [int]$env:TERMIXGO_DESKTOP_Y) | Out-Null
$down = [uint32]$env:TERMIXGO_DESKTOP_DOWN
$up = [uint32]$env:TERMIXGO_DESKTOP_UP
$count = [int]$env:TERMIXGO_DESKTOP_COUNT
for ($i = 0; $i -lt $count; $i++) {
  [TermixInput]::mouse_event($down, 0, 0, 0, [UIntPtr]::Zero)
  [TermixInput]::mouse_event($up, 0, 0, 0, [UIntPtr]::Zero)
}`

func (windowsController) Click(ctx context.Context, point Point, button string, count int) error {
	if count < 1 {
		count = 1
	}
	down, up := uint32(0x0002), uint32(0x0004)
	switch normalizeButton(button) {
	case "right":
		down, up = 0x0008, 0x0010
	case "middle":
		down, up = 0x0020, 0x0040
	}
	env := append(os.Environ(),
		"TERMIXGO_DESKTOP_X="+strconv.Itoa(point.X),
		"TERMIXGO_DESKTOP_Y="+strconv.Itoa(point.Y),
		fmt.Sprintf("TERMIXGO_DESKTOP_DOWN=%d", down),
		fmt.Sprintf("TERMIXGO_DESKTOP_UP=%d", up),
		"TERMIXGO_DESKTOP_COUNT="+strconv.Itoa(count),
	)
	return powershell(ctx, windowsClickScript, env)
}

const windowsDragScript = windowsInputPrelude + `[TermixInput]::SetCursorPos([int]$env:TERMIXGO_DESKTOP_X, [int]$env:TERMIXGO_DESKTOP_Y) | Out-Null
[TermixInput]::mouse_event(0x0002, 0, 0, 0, [UIntPtr]::Zero)
Start-Sleep -Milliseconds 120
[TermixInput]::SetCursorPos([int]$env:TERMIXGO_DESKTOP_X2, [int]$env:TERMIXGO_DESKTOP_Y2) | Out-Null
Start-Sleep -Milliseconds 120
[TermixInput]::mouse_event(0x0004, 0, 0, 0, [UIntPtr]::Zero)`

func (windowsController) Drag(ctx context.Context, from, to Point) error {
	env := append(os.Environ(),
		"TERMIXGO_DESKTOP_X="+strconv.Itoa(from.X),
		"TERMIXGO_DESKTOP_Y="+strconv.Itoa(from.Y),
		"TERMIXGO_DESKTOP_X2="+strconv.Itoa(to.X),
		"TERMIXGO_DESKTOP_Y2="+strconv.Itoa(to.Y),
	)
	return powershell(ctx, windowsDragScript, env)
}

const windowsScrollScript = windowsInputPrelude + `$delta = [int]$env:TERMIXGO_DESKTOP_DELTA
$count = [int]$env:TERMIXGO_DESKTOP_COUNT
for ($i = 0; $i -lt $count; $i++) {
  [TermixInput]::mouse_event(0x0800, 0, 0, [uint32]$delta, [UIntPtr]::Zero)
}`

func (windowsController) Scroll(ctx context.Context, direction string, amount int) error {
	if amount < 1 {
		amount = 1
	}
	if amount > 50 {
		amount = 50
	}
	delta := 120
	if strings.EqualFold(strings.TrimSpace(direction), "down") {
		delta = -120
	}
	env := append(os.Environ(),
		"TERMIXGO_DESKTOP_DELTA="+strconv.Itoa(delta),
		"TERMIXGO_DESKTOP_COUNT="+strconv.Itoa(amount),
	)
	return powershell(ctx, windowsScrollScript, env)
}

const windowsTypeScript = `Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.SendKeys]::SendWait($env:TERMIXGO_DESKTOP_TEXT)`

func (windowsController) TypeText(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	env := append(os.Environ(), "TERMIXGO_DESKTOP_TEXT="+escapeSendKeys(text))
	return powershell(ctx, windowsTypeScript, env)
}

const windowsPressScript = `Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.SendKeys]::SendWait($env:TERMIXGO_DESKTOP_TEXT)`

func (windowsController) PressKey(ctx context.Context, combo string) error {
	parts, err := normalizeKeyCombo(combo)
	if err != nil {
		return err
	}
	prefix := ""
	key := ""
	for _, part := range parts {
		switch part {
		case "ctrl":
			prefix += "^"
		case "alt":
			prefix += "%"
		case "shift":
			prefix += "+"
		case "cmd":
			return fmt.Errorf("the cmd modifier is not a Windows key: use ctrl")
		default:
			key = part
		}
	}
	if key == "" {
		return fmt.Errorf("a key is required after the modifiers")
	}
	encoded, ok := sendKeysKey(key)
	if !ok {
		return fmt.Errorf("unknown key %q", key)
	}
	env := append(os.Environ(), "TERMIXGO_DESKTOP_TEXT="+prefix+encoded)
	return powershell(ctx, windowsPressScript, env)
}

const windowsLaunchScript = `Start-Process -FilePath $env:TERMIXGO_DESKTOP_TARGET`

func (windowsController) Launch(ctx context.Context, target string, args []string) error {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return fmt.Errorf("an application name or file path is required")
	}
	if path, err := exec.LookPath(trimmed); err == nil {
		command := exec.CommandContext(ctx, path, args...)
		if err := command.Start(); err != nil {
			return fmt.Errorf("cannot start %s: %v", trimmed, err)
		}
		return nil
	}
	if len(args) > 0 {
		return fmt.Errorf("%s is not an executable on PATH, so it cannot take arguments", trimmed)
	}
	// The target travels through the environment, never through the command
	// line, so a character such as & cannot become shell syntax.
	env := append(os.Environ(), "TERMIXGO_DESKTOP_TARGET="+trimmed)
	return powershell(ctx, windowsLaunchScript, env)
}

// escapeSendKeys braces the characters SendKeys reads as control syntax, so a
// typed string reaches the field literally.
func escapeSendKeys(text string) string {
	var out strings.Builder
	out.Grow(len(text) + 8)
	for _, char := range text {
		switch char {
		case '+', '^', '%', '~', '(', ')', '{', '}', '[', ']':
			out.WriteString("{" + string(char) + "}")
		case '\n':
			out.WriteString("{ENTER}")
		case '\r':
			continue
		default:
			out.WriteRune(char)
		}
	}
	return out.String()
}

func sendKeysKey(key string) (string, bool) {
	named := map[string]string{
		"enter": "{ENTER}", "tab": "{TAB}", "esc": "{ESC}", "space": " ",
		"backspace": "{BACKSPACE}", "delete": "{DELETE}",
		"left": "{LEFT}", "right": "{RIGHT}", "up": "{UP}", "down": "{DOWN}",
		"home": "{HOME}", "end": "{END}", "pageup": "{PGUP}", "pagedown": "{PGDN}",
	}
	if value, ok := named[key]; ok {
		return value, true
	}
	if len([]rune(key)) == 1 {
		return escapeSendKeys(key), true
	}
	return "", false
}
