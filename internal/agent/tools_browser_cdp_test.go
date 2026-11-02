package agent

import (
	"context"
	"strings"
	"testing"
)

func TestLiveBrowserToolsAreRegistered(t *testing.T) {
	registry := DefaultRegistry()
	for _, name := range []string{
		"browser_navigate", "browser_read", "browser_click", "browser_type",
		"browser_press", "browser_scroll", "browser_wait_for", "browser_eval",
		"browser_tabs", "browser_cookies", "browser_close",
	} {
		if _, ok := registry.Lookup(name); !ok {
			t.Fatalf("%s is not registered", name)
		}
	}
}

func TestLiveBrowserToolsValidateBeforeOpening(t *testing.T) {
	env := testEnv(t)
	cases := []struct {
		name string
		tool Tool
		args map[string]any
		want string
	}{
		{"navigate", &browserNavigateTool{}, map[string]any{}, "url is required"},
		{"click", &browserClickTool{}, map[string]any{}, "selector, a text label or an x/y coordinate"},
		{"eval", &browserEvalTool{}, map[string]any{}, "expression is required"},
		{"wait", &browserWaitForTool{}, map[string]any{}, "selector, text or expression"},
		{"press", &browserPressTool{}, map[string]any{}, "key is required"},
		{"tabs", &browserTabsTool{}, map[string]any{"action": "teleport"}, "unknown action"},
		{"cookies", &browserCookiesTool{}, map[string]any{"action": "teleport"}, "unknown action"},
		{"cookies set", &browserCookiesTool{}, map[string]any{"action": "set"}, "name is required"},
	}
	for _, testCase := range cases {
		result, err := testCase.tool.Run(context.Background(), env, testCase.args)
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		if !result.IsError || !strings.Contains(result.Output, testCase.want) {
			t.Fatalf("%s output = %q, want %q", testCase.name, result.Output, testCase.want)
		}
	}
}

func TestBrowserCloseWithNoSession(t *testing.T) {
	result, err := (&browserCloseTool{}).Run(context.Background(), testEnv(t), map[string]any{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.IsError || !strings.Contains(result.Output, "No live browser") {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestBrowserScreenshotRequiresURLWithoutSession(t *testing.T) {
	env := testEnv(t)
	result, err := (&browserScreenshotTool{}).Run(context.Background(), env, map[string]any{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Output, "url is required") {
		t.Fatalf("output = %q", result.Output)
	}
}
