//go:build darwin

package desktop

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type darwinController struct{}

func newController() Controller { return darwinController{} }

// requirements lists the macOS helpers. screencapture is part of the system;
// cliclick is the only supported pointer driver beyond a click and a focus,
// and System Events covers typing.
func requirements() []string {
	var missing []string
	if !hasAny("screencapture") {
		missing = append(missing, "screencapture")
	}
	if !hasAny("cliclick") {
		missing = append(missing, "cliclick (brew install cliclick) for mouse movement, drag and scroll")
	}
	return missing
}

func hasAny(names ...string) bool {
	for _, name := range names {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}

func (darwinController) Capture(ctx context.Context, request CaptureRequest) error {
	if err := ensureOutputPath(request.Output); err != nil {
		return err
	}
	binary, err := requireTool("screencapture")
	if err != nil {
		return err
	}
	args := []string{"-x", "-t", "png"}
	if request.Window != "" {
		id := strings.TrimSpace(request.Window)
		if _, convErr := strconv.Atoi(id); convErr != nil {
			return fmt.Errorf("capturing one window needs its numeric CGWindowID; focus the window and capture the screen instead")
		}
		args = append(args, "-l", id)
	} else if request.Region != nil {
		region := request.Region
		args = append(args, "-R", fmt.Sprintf("%d,%d,%d,%d", region.X, region.Y, region.Width, region.Height))
	}
	args = append(args, request.Output)
	return run(ctx, binary, args...)
}

const darwinWindowScript = `on run argv
  set output to ""
  tell application "System Events"
    repeat with p in (every process whose background only is false)
      try
        set appName to name of p
        set pid to unix id of p
        repeat with w in windows of p
          try
            set output to output & pid & tab & appName & tab & (name of w) & linefeed
          end try
        end repeat
      end try
    end repeat
  end tell
  return output
end run`

func (darwinController) ListWindows(ctx context.Context) ([]Window, error) {
	binary, err := requireTool("osascript")
	if err != nil {
		return nil, err
	}
	raw, err := output(ctx, binary, "-e", darwinWindowScript)
	if err != nil {
		return nil, fmt.Errorf("listing windows needs Accessibility permission for the terminal: %w", err)
	}
	return parseWindows(raw), nil
}

const darwinFocusScript = `on run argv
  tell application "System Events"
    set targetProcess to first process whose name is (item 1 of argv)
    set frontmost of targetProcess to true
  end tell
end run`

func (darwinController) FocusWindow(ctx context.Context, query string) error {
	binary, err := requireTool("osascript")
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return fmt.Errorf("an application name is required")
	}
	return run(ctx, binary, "-e", darwinFocusScript, trimmed)
}

func (darwinController) Move(ctx context.Context, point Point) error {
	cliclick, err := requireTool("cliclick")
	if err != nil {
		return fmt.Errorf("moving the pointer needs cliclick: %w", err)
	}
	return run(ctx, cliclick, fmt.Sprintf("m:%d,%d", point.X, point.Y))
}

func (darwinController) Click(ctx context.Context, point Point, button string, count int) error {
	if count < 1 {
		count = 1
	}
	name := normalizeButton(button)
	if cliclick, err := exec.LookPath("cliclick"); err == nil {
		prefix := "c"
		switch name {
		case "right":
			prefix = "rc"
		case "middle":
			return fmt.Errorf("the middle button needs a driver cliclick does not provide")
		}
		for index := 0; index < count; index++ {
			if err := run(ctx, cliclick, fmt.Sprintf("%s:%d,%d", prefix, point.X, point.Y)); err != nil {
				return err
			}
		}
		return nil
	}
	if name != "left" {
		return fmt.Errorf("a %s click needs cliclick", name)
	}
	return darwinSystemEvents(ctx, darwinClickScript, strconv.Itoa(point.X), strconv.Itoa(point.Y))
}

const darwinClickScript = `on run argv
  tell application "System Events" to click at {(item 1 of argv) as integer, (item 2 of argv) as integer}
end run`

func (darwinController) Drag(ctx context.Context, from, to Point) error {
	cliclick, err := requireTool("cliclick")
	if err != nil {
		return fmt.Errorf("dragging needs cliclick: %w", err)
	}
	return run(ctx, cliclick,
		fmt.Sprintf("dd:%d,%d", from.X, from.Y),
		fmt.Sprintf("dm:%d,%d", to.X, to.Y),
		fmt.Sprintf("du:%d,%d", to.X, to.Y))
}

func (darwinController) Scroll(_ context.Context, _ string, _ int) error {
	return fmt.Errorf("scrolling is not supported on macOS without a driver: use keyboard_press pageup or pagedown instead")
}

func (darwinController) TypeText(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	for index, line := range strings.Split(text, "\n") {
		if index > 0 {
			if err := darwinSystemEvents(ctx, darwinReturnScript); err != nil {
				return err
			}
		}
		if line == "" {
			continue
		}
		if err := darwinSystemEvents(ctx, darwinTypeScript, line); err != nil {
			return err
		}
	}
	return nil
}

const darwinTypeScript = `on run argv
  tell application "System Events" to keystroke (item 1 of argv)
end run`

const darwinReturnScript = `tell application "System Events" to key code 36`

// darwinKeyCodes maps the named keys System Events addresses by code.
var darwinKeyCodes = map[string]int{
	"enter": 36, "tab": 48, "space": 49, "delete": 51, "esc": 53,
	"left": 123, "right": 124, "down": 125, "up": 126,
	"home": 115, "end": 119, "pageup": 116, "pagedown": 121,
}

func (darwinController) PressKey(ctx context.Context, combo string) error {
	parts, err := normalizeKeyCombo(combo)
	if err != nil {
		return err
	}
	modifiers := map[string]bool{}
	key := ""
	for _, part := range parts {
		switch part {
		case "ctrl", "alt", "cmd", "shift":
			modifiers[part] = true
		default:
			key = part
		}
	}
	if key == "" {
		return fmt.Errorf("a key is required after the modifiers")
	}
	modifierList := ""
	add := func(name string) {
		if modifierList != "" {
			modifierList += ", "
		}
		modifierList += name
	}
	if modifiers["cmd"] {
		add("command down")
	}
	if modifiers["ctrl"] {
		add("control down")
	}
	if modifiers["alt"] {
		add("option down")
	}
	if modifiers["shift"] {
		add("shift down")
	}
	using := ""
	if modifierList != "" {
		using = " using {" + modifierList + "}"
	}
	if code, ok := darwinKeyCodes[key]; ok {
		script := fmt.Sprintf(`tell application "System Events" to key code %d%s`, code, using)
		return darwinSystemEvents(ctx, script)
	}
	if len([]rune(key)) == 1 {
		script := fmt.Sprintf(`on run argv
  tell application "System Events" to keystroke (item 1 of argv)%s
end run`, using)
		return darwinSystemEvents(ctx, script, key)
	}
	return fmt.Errorf("unknown key %q", key)
}

func (darwinController) Launch(ctx context.Context, target string, args []string) error {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return fmt.Errorf("an application name or file path is required")
	}
	opener, err := requireTool("open")
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return run(ctx, opener, append([]string{"-a", trimmed, "--args"}, args...)...)
	}
	return run(ctx, opener, trimmed)
}

func darwinSystemEvents(ctx context.Context, script string, argv ...string) error {
	binary, err := requireTool("osascript")
	if err != nil {
		return err
	}
	args := []string{"-e", script}
	if len(argv) > 0 {
		args = append(args, argv...)
	}
	return run(ctx, binary, args...)
}
