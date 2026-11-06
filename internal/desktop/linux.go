//go:build linux

package desktop

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type linuxController struct{}

func newController() Controller { return linuxController{} }

// requirements lists the driver each part of the surface needs. X11 uses
// xdotool and wmctrl; Wayland uses ydotool for input and grim for capture.
func requirements() []string {
	var missing []string
	if !hasAny("xdotool", "ydotool") {
		missing = append(missing, "xdotool or ydotool")
	}
	if !hasAny("grim", "import", "scrot", "gnome-screenshot") {
		missing = append(missing, "grim, imagemagick or scrot")
	}
	if !hasAny("wmctrl", "xdotool") {
		missing = append(missing, "wmctrl")
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

func (linuxController) Capture(ctx context.Context, request CaptureRequest) error {
	if err := ensureOutputPath(request.Output); err != nil {
		return err
	}
	if request.Window != "" {
		importBin, err := requireTool("import")
		if err != nil {
			return fmt.Errorf("capturing one window needs ImageMagick (import): %w", err)
		}
		windowID, err := linuxWindowID(ctx, request.Window)
		if err != nil {
			return err
		}
		return run(ctx, importBin, "-window", windowID, request.Output)
	}
	if request.Region != nil {
		region := request.Region
		if grim, err := exec.LookPath("grim"); err == nil {
			return run(ctx, grim, "-g", fmt.Sprintf("%d,%d %dx%d", region.X, region.Y, region.Width, region.Height), request.Output)
		}
		if scrot, err := exec.LookPath("scrot"); err == nil {
			return run(ctx, scrot, "-a", fmt.Sprintf("%d,%d,%d,%d", region.X, region.Y, region.Width, region.Height), request.Output)
		}
		importBin, err := requireTool("import")
		if err != nil {
			return err
		}
		return run(ctx, importBin, "-window", "root", "-crop", fmt.Sprintf("%dx%d+%d+%d", region.Width, region.Height, region.X, region.Y), request.Output)
	}
	if grim, err := exec.LookPath("grim"); err == nil {
		return run(ctx, grim, request.Output)
	}
	if importBin, err := exec.LookPath("import"); err == nil {
		return run(ctx, importBin, "-window", "root", request.Output)
	}
	if scrot, err := exec.LookPath("scrot"); err == nil {
		return run(ctx, scrot, request.Output)
	}
	if screenshot, err := exec.LookPath("gnome-screenshot"); err == nil {
		return run(ctx, screenshot, "-f", request.Output)
	}
	return requirementError("screen capture", "grim", "import (imagemagick)", "scrot", "gnome-screenshot")
}

func (linuxController) ListWindows(ctx context.Context) ([]Window, error) {
	if wmctrl, err := exec.LookPath("wmctrl"); err == nil {
		raw, err := output(ctx, wmctrl, "-lG")
		if err != nil {
			return nil, err
		}
		var windows []Window
		for _, line := range strings.Split(raw, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 6 {
				continue
			}
			windows = append(windows, Window{
				ID:    fields[0],
				Title: strings.Join(fields[6:], " "),
				App:   fields[5],
			})
		}
		return windows, nil
	}
	xdotool, err := requireTool("wmctrl", "xdotool")
	if err != nil {
		return nil, err
	}
	raw, err := output(ctx, xdotool, "search", "--onlyvisible", "--name", "")
	if err != nil {
		return nil, fmt.Errorf("listing windows needs wmctrl or a window name for xdotool: %w", err)
	}
	var windows []Window
	for _, id := range strings.Fields(raw) {
		title, _ := output(ctx, xdotool, "getwindowname", id)
		windows = append(windows, Window{ID: id, Title: strings.TrimSpace(title)})
	}
	return windows, nil
}

func (linuxController) FocusWindow(ctx context.Context, query string) error {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return fmt.Errorf("a window title or id is required")
	}
	if wmctrl, err := exec.LookPath("wmctrl"); err == nil {
		return run(ctx, wmctrl, "-a", trimmed)
	}
	xdotool, err := requireTool("wmctrl", "xdotool")
	if err != nil {
		return err
	}
	windowID, err := linuxWindowID(ctx, trimmed)
	if err != nil {
		return err
	}
	return run(ctx, xdotool, "windowactivate", "--sync", windowID)
}

func (linuxController) Move(ctx context.Context, point Point) error {
	if xdotool, err := exec.LookPath("xdotool"); err == nil {
		return run(ctx, xdotool, "mousemove", "--sync", strconv.Itoa(point.X), strconv.Itoa(point.Y))
	}
	ydotool, err := requireTool("xdotool", "ydotool")
	if err != nil {
		return err
	}
	return run(ctx, ydotool, "mousemove", "--absolute", "-x", strconv.Itoa(point.X), "-y", strconv.Itoa(point.Y))
}

func (linuxController) Click(ctx context.Context, point Point, button string, count int) error {
	if count < 1 {
		count = 1
	}
	name := normalizeButton(button)
	if xdotool, err := exec.LookPath("xdotool"); err == nil {
		number := "1"
		switch name {
		case "right":
			number = "3"
		case "middle":
			number = "2"
		}
		return run(ctx, xdotool, "mousemove", "--sync", strconv.Itoa(point.X), strconv.Itoa(point.Y),
			"click", "--repeat", strconv.Itoa(count), number)
	}
	ydotool, err := requireTool("xdotool", "ydotool")
	if err != nil {
		return err
	}
	code := "0xC0"
	switch name {
	case "right":
		code = "0xC1"
	case "middle":
		code = "0xC2"
	}
	if err := run(ctx, ydotool, "mousemove", "--absolute", "-x", strconv.Itoa(point.X), "-y", strconv.Itoa(point.Y)); err != nil {
		return err
	}
	for index := 0; index < count; index++ {
		if err := run(ctx, ydotool, "click", code); err != nil {
			return err
		}
	}
	return nil
}

func (linuxController) Drag(ctx context.Context, from, to Point) error {
	xdotool, err := requireTool("xdotool")
	if err != nil {
		return fmt.Errorf("dragging needs xdotool: %w", err)
	}
	return run(ctx, xdotool,
		"mousemove", "--sync", strconv.Itoa(from.X), strconv.Itoa(from.Y),
		"mousedown", "1",
		"mousemove", "--sync", strconv.Itoa(to.X), strconv.Itoa(to.Y),
		"mouseup", "1")
}

func (linuxController) Scroll(ctx context.Context, direction string, amount int) error {
	if amount < 1 {
		amount = 1
	}
	if amount > 50 {
		amount = 50
	}
	button := "4"
	if strings.EqualFold(strings.TrimSpace(direction), "down") {
		button = "5"
	}
	xdotool, err := requireTool("xdotool")
	if err != nil {
		return fmt.Errorf("scrolling needs xdotool: %w", err)
	}
	return run(ctx, xdotool, "click", "--repeat", strconv.Itoa(amount), button)
}

func (linuxController) TypeText(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	if xdotool, err := exec.LookPath("xdotool"); err == nil {
		for index, line := range strings.Split(text, "\n") {
			if index > 0 {
				if err := run(ctx, xdotool, "key", "--clearmodifiers", "Return"); err != nil {
					return err
				}
			}
			if line == "" {
				continue
			}
			if err := run(ctx, xdotool, "type", "--clearmodifiers", "--delay", "8", "--", line); err != nil {
				return err
			}
		}
		return nil
	}
	ydotool, err := requireTool("xdotool", "ydotool")
	if err != nil {
		return err
	}
	return run(ctx, ydotool, "type", "--", text)
}

func (linuxController) PressKey(ctx context.Context, combo string) error {
	parts, err := normalizeKeyCombo(combo)
	if err != nil {
		return err
	}
	mapped := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "cmd" {
			part = "super"
		}
		mapped = append(mapped, part)
	}
	xdotool, err := requireTool("xdotool")
	if err != nil {
		return fmt.Errorf("a key press needs xdotool: %w", err)
	}
	return run(ctx, xdotool, "key", "--clearmodifiers", strings.Join(mapped, "+"))
}

func (linuxController) Launch(ctx context.Context, target string, args []string) error {
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
	opener, err := requireTool("xdg-open")
	if err != nil {
		return err
	}
	if len(args) > 0 {
		return fmt.Errorf("%s is not an executable on PATH, so it cannot take arguments; pass a file path or an installed program name", trimmed)
	}
	return run(ctx, opener, trimmed)
}

// linuxWindowID resolves a title or id to an xdotool window id.
func linuxWindowID(ctx context.Context, query string) (string, error) {
	xdotool, err := requireTool("xdotool")
	if err != nil {
		return "", err
	}
	raw, err := output(ctx, xdotool, "search", "--name", query)
	if err != nil {
		return "", fmt.Errorf("no window matches %q", query)
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return "", fmt.Errorf("no window matches %q", query)
	}
	return fields[len(fields)-1], nil
}
