package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/desktop"
)

// DesktopToolNames lists the tools that drive the local machine. The app
// removes them from the registry unless the operator turned desktopControl on,
// and no guest ever receives them.
var DesktopToolNames = []string{"desktop_screenshot", "desktop_input", "desktop_windows"}

// DesktopTools returns the desktop surface. The app merges it back into the
// live registry when the operator enables the capability without a restart.
func DesktopTools() []Tool {
	return []Tool{&desktopScreenshotTool{}, &desktopInputTool{}, &desktopWindowsTool{}}
}

// desktopEnabled is the one gate every desktop tool checks. The registry is
// already filtered, but a tool that slips through must still refuse: the
// capability is opt-in, and a config read is the proof.
func desktopEnabled(env *Env) (bool, string) {
	if env != nil && env.Config.DesktopControl {
		return true, ""
	}
	return false, "Desktop control is off. Turn it on in ~/.termixgo/config.json with {\"desktopControl\": true} (or /desktop on) and restart the turn."
}

func desktopRequirementNote() string {
	if missing := desktop.Requirements(); len(missing) > 0 {
		return " This host is missing: " + strings.Join(missing, ", ") + "."
	}
	return ""
}

// desktopScreenshotTool captures the screen, a region or one window.
type desktopScreenshotTool struct{}

func (t *desktopScreenshotTool) Name() string { return "desktop_screenshot" }
func (t *desktopScreenshotTool) Aliases() []string {
	return []string{"screen_capture", "screenshot_screen", "capture_screen"}
}
func (t *desktopScreenshotTool) Mutating() bool { return true }
func (t *desktopScreenshotTool) Risk() Risk     { return RiskEdit }
func (t *desktopScreenshotTool) Label(a map[string]any) string {
	return "Capturing the desktop"
}
func (t *desktopScreenshotTool) DoneLabel(a map[string]any) string {
	return "Captured the desktop"
}
func (t *desktopScreenshotTool) Description() string {
	return "Capture the local machine's screen, a rectangle, or one window to a PNG, then inspect it with read_image. This is the way to see what an application on the desktop is showing."
}
func (t *desktopScreenshotTool) Schema() map[string]any {
	return object(map[string]any{
		"target":      strProp("What to capture: screen (default), region, or window."),
		"window":      strProp("Window title or id, used with target=window."),
		"x":           intProp("Region left edge in pixels, used with target=region."),
		"y":           intProp("Region top edge in pixels, used with target=region."),
		"width":       intProp("Region width in pixels, used with target=region."),
		"height":      intProp("Region height in pixels, used with target=region."),
		"output_path": strProp("Optional PNG path; defaults to .termixgo/screenshots/desktop-<timestamp>.png."),
	}, "target")
}

func (t *desktopScreenshotTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if ok, reason := desktopEnabled(env); !ok {
		return Result{Output: reason, IsError: true}, nil
	}
	target := strings.ToLower(strings.TrimSpace(argString(args, "target")))
	if target == "" {
		target = "screen"
	}

	request := desktop.CaptureRequest{Window: strings.TrimSpace(argString(args, "window"))}
	switch target {
	case "screen":
	case "window":
		if request.Window == "" {
			return Result{Output: "window is required when target=window", IsError: true}, nil
		}
	case "region":
		request.Region = &desktop.Region{
			X:      argInt(args, "x", 0, -20000, 20000),
			Y:      argInt(args, "y", 0, -20000, 20000),
			Width:  argInt(args, "width", 800, 1, 20000),
			Height: argInt(args, "height", 600, 1, 20000),
		}
	default:
		return Result{Output: fmt.Sprintf("target must be screen, region or window, not %q", target), IsError: true}, nil
	}

	outPath := strings.TrimSpace(argString(args, "output_path"))
	if outPath == "" {
		dir := filepath.Join(env.Workspace, ".termixgo", "screenshots")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Result{Output: "Cannot create the screenshot directory: " + err.Error(), IsError: true}, nil
		}
		outPath = filepath.Join(dir, fmt.Sprintf("desktop-%d.png", time.Now().Unix()))
	} else {
		outPath = resolvePath(env, outPath)
	}
	if err := checkWorkspacePath(env, outPath); err != nil {
		return Result{Output: fmt.Sprintf("cannot write a screenshot outside the workspace: %v", err), IsError: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return Result{Output: "Cannot create the screenshot directory: " + err.Error(), IsError: true}, nil
	}
	request.Output = outPath

	if err := desktop.New().Capture(ctx, request); err != nil {
		return Result{Output: fmt.Sprintf("Screenshot failed: %v.%s", err, desktopRequirementNote()), IsError: true}, nil
	}
	info, statErr := os.Stat(outPath)
	if statErr != nil {
		return Result{Output: "The capture command succeeded but wrote no file.", IsError: true}, nil
	}
	rel := displayPath(env, outPath)
	return Result{Output: fmt.Sprintf("Captured the %s to %s (%d bytes). Call read_image with path %q to see it.", target, rel, info.Size(), rel)}, nil
}

// desktopInputTool drives the pointer and the keyboard.
type desktopInputTool struct{}

func (t *desktopInputTool) Name() string { return "desktop_input" }
func (t *desktopInputTool) Aliases() []string {
	return []string{"mouse_click", "mouse_move", "keyboard_type", "press_key", "computer_use"}
}
func (t *desktopInputTool) Mutating() bool { return true }
func (t *desktopInputTool) Risk() Risk     { return RiskEdit }
func (t *desktopInputTool) Label(a map[string]any) string {
	action := strings.TrimSpace(argString(a, "action"))
	if action == "" {
		action = "input"
	}
	return "Desktop " + action
}
func (t *desktopInputTool) DoneLabel(a map[string]any) string {
	action := strings.TrimSpace(argString(a, "action"))
	if action == "" {
		action = "input"
	}
	return "Desktop " + action + " done"
}
func (t *desktopInputTool) Description() string {
	return "Control the local machine: move the pointer, click, double click, right click, drag, scroll, type text, or press a key combo in the focused application. Take a screenshot first and again afterwards so you can see the result."
}
func (t *desktopInputTool) Schema() map[string]any {
	return object(map[string]any{
		"action":    strProp("One of: move, click, double_click, right_click, middle_click, drag, scroll, type, key."),
		"x":         intProp("Pointer X in pixels, for move, click and drag."),
		"y":         intProp("Pointer Y in pixels, for move, click and drag."),
		"x2":        intProp("Drag end X in pixels."),
		"y2":        intProp("Drag end Y in pixels."),
		"direction": strProp("Scroll direction: up (default) or down."),
		"amount":    intProp("Scroll notches, 1 to 50, or the click count for click."),
		"text":      strProp("Text to type, for action=type. Newlines press Enter."),
		"key":       strProp("Key or combo to press, for action=key: enter, tab, esc, ctrl+s, cmd+space, alt+tab."),
	}, "action")
}

func (t *desktopInputTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if ok, reason := desktopEnabled(env); !ok {
		return Result{Output: reason, IsError: true}, nil
	}
	action := strings.ToLower(strings.TrimSpace(argString(args, "action")))
	if action == "" {
		return Result{Output: "action is required", IsError: true}, nil
	}
	controller := desktop.New()
	point := desktop.Point{
		X: argInt(args, "x", 0, -20000, 20000),
		Y: argInt(args, "y", 0, -20000, 20000),
	}

	var err error
	switch action {
	case "move":
		err = controller.Move(ctx, point)
	case "click", "double_click", "right_click", "middle_click":
		count := argInt(args, "amount", 1, 1, 3)
		if action == "double_click" {
			count = 2
		}
		button := "left"
		switch action {
		case "right_click":
			button = "right"
		case "middle_click":
			button = "middle"
		}
		err = controller.Click(ctx, point, button, count)
	case "drag":
		end := desktop.Point{
			X: argInt(args, "x2", 0, -20000, 20000),
			Y: argInt(args, "y2", 0, -20000, 20000),
		}
		err = controller.Drag(ctx, point, end)
	case "scroll":
		err = controller.Scroll(ctx, argString(args, "direction"), argInt(args, "amount", 3, 1, 50))
	case "type":
		text := argString(args, "text")
		if text == "" {
			return Result{Output: "text is required for action=type", IsError: true}, nil
		}
		err = controller.TypeText(ctx, text)
	case "key":
		key := argString(args, "key")
		if strings.TrimSpace(key) == "" {
			return Result{Output: "key is required for action=key", IsError: true}, nil
		}
		err = controller.PressKey(ctx, key)
	default:
		return Result{Output: fmt.Sprintf("unknown action %q: use move, click, double_click, right_click, middle_click, drag, scroll, type or key", action), IsError: true}, nil
	}
	if err != nil {
		return Result{Output: fmt.Sprintf("Desktop %s failed: %v.%s", action, err, desktopRequirementNote()), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Desktop %s completed. Take another desktop_screenshot to confirm the result.", action)}, nil
}

// desktopWindowsTool lists, focuses and launches applications.
type desktopWindowsTool struct{}

func (t *desktopWindowsTool) Name() string { return "desktop_windows" }
func (t *desktopWindowsTool) Aliases() []string {
	return []string{"window_list", "window_focus", "app_launch", "list_windows"}
}
func (t *desktopWindowsTool) Mutating() bool { return true }
func (t *desktopWindowsTool) Risk() Risk     { return RiskEdit }
func (t *desktopWindowsTool) Label(a map[string]any) string {
	action := strings.TrimSpace(argString(a, "action"))
	if action == "" {
		action = "list"
	}
	return "Desktop windows " + action
}
func (t *desktopWindowsTool) DoneLabel(a map[string]any) string {
	action := strings.TrimSpace(argString(a, "action"))
	if action == "" {
		action = "list"
	}
	return "Desktop windows " + action
}
func (t *desktopWindowsTool) Description() string {
	return "List the local machine's open windows, bring one to the front, or launch an application or file. Focus the target window before desktop_input so the keystrokes land in the right application."
}
func (t *desktopWindowsTool) Schema() map[string]any {
	return object(map[string]any{
		"action": strProp("One of: list, focus, launch."),
		"query":  strProp("Window title, process name or id, for action=focus."),
		"app":    strProp("Application name on PATH, or a file path, for action=launch."),
		"args":   arrayProp("Optional arguments passed to a launched program.", strProp("One argument.")),
	}, "action")
}

func (t *desktopWindowsTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if ok, reason := desktopEnabled(env); !ok {
		return Result{Output: reason, IsError: true}, nil
	}
	action := strings.ToLower(strings.TrimSpace(argString(args, "action")))
	if action == "" {
		action = "list"
	}
	controller := desktop.New()
	switch action {
	case "list":
		windows, err := controller.ListWindows(ctx)
		if err != nil {
			return Result{Output: fmt.Sprintf("Listing windows failed: %v.%s", err, desktopRequirementNote()), IsError: true}, nil
		}
		if len(windows) == 0 {
			return Result{Output: "No application window is open."}, nil
		}
		var out strings.Builder
		for index, window := range windows {
			if index >= 50 {
				fmt.Fprintf(&out, "... (%d more windows elided)\n", len(windows)-50)
				break
			}
			fmt.Fprintf(&out, "[%s] %s: %s\n", window.ID, window.App, window.Title)
		}
		return Result{Output: out.String()}, nil
	case "focus":
		query := strings.TrimSpace(argString(args, "query"))
		if query == "" {
			return Result{Output: "query is required for action=focus", IsError: true}, nil
		}
		if err := controller.FocusWindow(ctx, query); err != nil {
			return Result{Output: fmt.Sprintf("Focusing %q failed: %v.%s", query, err, desktopRequirementNote()), IsError: true}, nil
		}
		return Result{Output: fmt.Sprintf("Focused %q. Take a desktop_screenshot to confirm.", query)}, nil
	case "launch":
		app := strings.TrimSpace(argString(args, "app"))
		if app == "" {
			return Result{Output: "app is required for action=launch", IsError: true}, nil
		}
		if err := controller.Launch(ctx, app, argList(args, "args")); err != nil {
			return Result{Output: fmt.Sprintf("Launching %q failed: %v.%s", app, err, desktopRequirementNote()), IsError: true}, nil
		}
		return Result{Output: fmt.Sprintf("Requested launch of %q. List the windows and take a screenshot to confirm it started.", app)}, nil
	default:
		return Result{Output: fmt.Sprintf("unknown action %q: use list, focus or launch", action), IsError: true}, nil
	}
}
