// Package desktop controls the local machine's screen and input: a screenshot,
// a mouse click, a key press, or launching an application. It shells out to
// the platform's own tools with explicit argv, never a shell string, because
// every coordinate and every typed character comes from the model.
//
// Nothing here is a substitute for a sandbox: these calls can do whatever the
// operator's account can do. The agent offers the tools only when the operator
// set desktopControl in the config, and every call is approval gated.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Point is a screen coordinate in pixels, origin at the top left.
type Point struct {
	X int
	Y int
}

// Region is a capture rectangle.
type Region struct {
	X      int
	Y      int
	Width  int
	Height int
}

// Window is one visible top-level window.
type Window struct {
	ID    string
	App   string
	Title string
}

// CaptureRequest selects what to capture. An empty Window and a nil Region
// mean the whole screen.
type CaptureRequest struct {
	Output string
	Window string
	Region *Region
}

// Controller is the host's desktop surface. Each method returns an error that
// names the missing tool when the host cannot perform it, so a machine without
// an input driver can still take screenshots.
type Controller interface {
	Capture(ctx context.Context, request CaptureRequest) error
	ListWindows(ctx context.Context) ([]Window, error)
	FocusWindow(ctx context.Context, query string) error
	Move(ctx context.Context, point Point) error
	Click(ctx context.Context, point Point, button string, count int) error
	Drag(ctx context.Context, from, to Point) error
	Scroll(ctx context.Context, direction string, amount int) error
	TypeText(ctx context.Context, text string) error
	PressKey(ctx context.Context, combo string) error
	Launch(ctx context.Context, target string, args []string) error
}

// commandTimeout bounds one desktop helper. A window manager that stops
// answering must not hang the turn.
const commandTimeout = 20 * time.Second

// New returns the controller for the running host.
func New() Controller { return newController() }

// Available reports whether the host has every driver desktop control uses.
func Available() bool { return len(Requirements()) == 0 }

// Requirements names the host tools that are missing, so the operator is told
// which package to install. An empty slice means the full surface works.
func Requirements() []string { return requirements() }

// requirementError names what the host is missing, so the operator is told
// which package to install rather than reading a bare exec failure.
func requirementError(what string, names ...string) error {
	if len(names) == 0 {
		return fmt.Errorf("%s is not supported on this host", what)
	}
	return fmt.Errorf("%s needs one of: %s", what, strings.Join(names, ", "))
}

// requireTool resolves the first available helper.
func requireTool(names ...string) (string, error) {
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", requirementError("this action", names...)
}

// run executes one helper with explicit argv and a bounded time.
func run(ctx context.Context, binary string, args ...string) error {
	runCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	command := exec.CommandContext(runCtx, binary, args...)
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%s failed: %s", name(binary), detail)
	}
	return nil
}

// output runs one helper and returns its stdout.
func output(ctx context.Context, binary string, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	command := exec.CommandContext(runCtx, binary, args...)
	var stderr strings.Builder
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("%s failed: %s", name(binary), detail)
	}
	return string(out), nil
}

// outputEnv runs one helper with an extra environment entry, which is how text
// reaches a PowerShell script without becoming script text.
func outputEnv(ctx context.Context, binary string, env []string, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	command := exec.CommandContext(runCtx, binary, args...)
	command.Env = env
	var stderr strings.Builder
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("%s failed: %s", name(binary), detail)
	}
	return string(out), nil
}

func name(binary string) string {
	if index := strings.LastIndexAny(binary, `/\`); index >= 0 {
		return binary[index+1:]
	}
	return binary
}

// parseWindows splits a tab-separated window listing.
func parseWindows(raw string) []Window {
	var windows []Window
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		window := Window{}
		if len(fields) > 0 {
			window.ID = strings.TrimSpace(fields[0])
		}
		if len(fields) > 1 {
			window.App = strings.TrimSpace(fields[1])
		}
		if len(fields) > 2 {
			window.Title = strings.TrimSpace(fields[2])
		}
		if window.Title == "" && window.App == "" {
			continue
		}
		windows = append(windows, window)
	}
	return windows
}

// normalizeButton maps the tool vocabulary onto the platform driver's.
func normalizeButton(button string) string {
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "right":
		return "right"
	case "middle":
		return "middle"
	default:
		return "left"
	}
}

// normalizeKeyCombo accepts "ctrl+shift+s", "cmd+s" and "enter", and returns a
// lowercase plus-joined form the platform layer maps.
func normalizeKeyCombo(combo string) ([]string, error) {
	trimmed := strings.TrimSpace(combo)
	if trimmed == "" {
		return nil, errors.New("key is required")
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool { return r == '+' || r == ' ' })
	if len(parts) == 0 {
		return nil, fmt.Errorf("cannot read key %q", combo)
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		switch part {
		case "control":
			part = "ctrl"
		case "option":
			part = "alt"
		case "command", "super", "win", "meta":
			part = "cmd"
		case "return":
			part = "enter"
		case "escape":
			part = "esc"
		case "del":
			part = "delete"
		}
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cannot read key %q", combo)
	}
	return out, nil
}

// ensureOutputPath rejects an empty capture target before a helper runs.
func ensureOutputPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("an output path is required for a screenshot")
	}
	return nil
}
