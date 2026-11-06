package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestDesktopToolsAreGatedOff(t *testing.T) {
	env := &Env{Workspace: t.TempDir()}
	result, err := (&desktopInputTool{}).Run(context.Background(), env, map[string]any{"action": "move", "x": 1, "y": 1})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Output, "Desktop control is off") {
		t.Fatalf("expected the gate message, got %q", result.Output)
	}
	if _, err := (&desktopScreenshotTool{}).Run(context.Background(), env, map[string]any{}); err != nil {
		t.Fatalf("screenshot run: %v", err)
	}
	if _, err := (&desktopWindowsTool{}).Run(context.Background(), env, map[string]any{"action": "list"}); err != nil {
		t.Fatalf("windows run: %v", err)
	}
}

func TestDesktopInputRejectsUnknownAction(t *testing.T) {
	env := &Env{Workspace: t.TempDir(), Config: config.Config{DesktopControl: true}}
	result, err := (&desktopInputTool{}).Run(context.Background(), env, map[string]any{"action": "teleport"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Output, "unknown action") {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestDesktopToolsAreRegistered(t *testing.T) {
	registry := DefaultRegistry()
	for _, name := range DesktopToolNames {
		if _, ok := registry.Lookup(name); !ok {
			t.Fatalf("%s is not registered", name)
		}
	}
	if len(DesktopTools()) != len(DesktopToolNames) {
		t.Fatalf("DesktopTools returns %d tools for %d names", len(DesktopTools()), len(DesktopToolNames))
	}
}
