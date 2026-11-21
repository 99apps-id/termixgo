package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/browser"
)

type fakeFlowBrowser struct {
	calls  []string
	failOn string
}

func (f *fakeFlowBrowser) record(call string) error {
	f.calls = append(f.calls, call)
	if f.failOn != "" && strings.HasPrefix(call, f.failOn) {
		return errors.New("element not found")
	}
	return nil
}
func (f *fakeFlowBrowser) Navigate(_ context.Context, url string) (string, error) {
	return "Loaded " + url, f.record("navigate " + url)
}
func (f *fakeFlowBrowser) Click(_ context.Context, selector, text string, _, _ float64) error {
	return f.record("click " + selector + text)
}
func (f *fakeFlowBrowser) Type(_ context.Context, selector, text string, _, submit bool) error {
	return f.record(fmt.Sprintf("type %s=%s submit=%v", selector, text, submit))
}
func (f *fakeFlowBrowser) Press(_ context.Context, combo string) error {
	return f.record("press " + combo)
}
func (f *fakeFlowBrowser) Scroll(_ context.Context, direction string, _ int) error {
	return f.record("scroll " + direction)
}
func (f *fakeFlowBrowser) WaitFor(_ context.Context, selector, text, _ string, _ time.Duration) error {
	return f.record("wait " + selector + text)
}
func (f *fakeFlowBrowser) Read(_ context.Context, mode, _ string) (string, error) {
	return "Welcome back", f.record("read " + mode)
}
func (f *fakeFlowBrowser) Screenshot(context.Context, bool, string) ([]byte, error) {
	return []byte("\x89PNG"), f.record("screenshot")
}

func useFakeBrowser(t *testing.T, fake *fakeFlowBrowser) {
	t.Helper()
	original := openFlowBrowser
	openFlowBrowser = func(context.Context) (flowBrowser, error) { return fake, nil }
	t.Cleanup(func() { openFlowBrowser = original })
}

func loginSteps() []any {
	return []any{
		map[string]any{"action": "navigate", "url": "https://example.test/login"},
		map[string]any{"action": "type", "selector": "#user", "value": "{{user}}"},
		map[string]any{"action": "type", "selector": "#pass", "value": "{{password}}", "submit": true},
		map[string]any{"action": "wait", "text": "Welcome"},
		map[string]any{"action": "read"},
	}
}

func TestBrowserStepsRunSaveAndReplay(t *testing.T) {
	fake := &fakeFlowBrowser{}
	useFakeBrowser(t, fake)
	env := &Env{Workspace: t.TempDir()}
	tool := &browserRunStepsTool{}

	result, _ := tool.Run(context.Background(), env, map[string]any{
		"steps":   loginSteps(),
		"vars":    map[string]any{"user": "ana", "password": "s3cret"},
		"save_as": "login",
	})
	if result.IsError || !strings.Contains(result.Output, "Welcome back") || !strings.Contains(result.Output, "Saved the flow") {
		t.Fatalf("result = %+v", result)
	}
	if strings.Contains(result.Output, "s3cret") {
		t.Fatalf("a typed value must not be echoed:\n%s", result.Output)
	}
	if fake.calls[2] != "type #pass=s3cret submit=true" {
		t.Fatalf("calls = %v", fake.calls)
	}
	saved, _ := os.ReadFile(filepath.Join(env.Workspace, ".termixgo", "flows", "login.json"))
	if strings.Contains(string(saved), "s3cret") || !strings.Contains(string(saved), "{{password}}") {
		t.Fatalf("the saved flow must keep placeholders, not values:\n%s", saved)
	}

	listed, _ := tool.Run(context.Background(), env, map[string]any{"list": true})
	if !strings.Contains(listed.Output, "login (5 steps) vars: password, user") {
		t.Fatalf("list = %q", listed.Output)
	}

	fake.calls = nil
	replay, _ := tool.Run(context.Background(), env, map[string]any{"flow": "login", "vars": map[string]any{"user": "budi", "password": "x"}})
	if replay.IsError || fake.calls[1] != "type #user=budi submit=false" {
		t.Fatalf("replay = %+v calls = %v", replay, fake.calls)
	}
}

func TestBrowserStepsStopAtTheFirstFailure(t *testing.T) {
	fake := &fakeFlowBrowser{failOn: "click"}
	useFakeBrowser(t, fake)
	env := &Env{Workspace: t.TempDir()}
	result, _ := (&browserRunStepsTool{}).Run(context.Background(), env, map[string]any{"steps": []any{
		map[string]any{"action": "navigate", "url": "https://example.test"},
		map[string]any{"action": "click", "text": "Buy"},
		map[string]any{"action": "read"},
	}})
	if !result.IsError || !strings.Contains(result.Output, "2. click FAILED") || !strings.Contains(result.Output, "Stopped at step 2 of 3") {
		t.Fatalf("result = %+v", result)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("no step should run after the failure: %v", fake.calls)
	}
}

func TestBrowserStepsRefuseAMissingVariable(t *testing.T) {
	fake := &fakeFlowBrowser{}
	useFakeBrowser(t, fake)
	env := &Env{Workspace: t.TempDir()}
	result, _ := (&browserRunStepsTool{}).Run(context.Background(), env, map[string]any{"steps": loginSteps(), "vars": map[string]any{"user": "ana"}})
	if !result.IsError || !strings.Contains(result.Output, "missing value for password") {
		t.Fatalf("result = %+v", result)
	}
	for _, call := range fake.calls {
		if strings.Contains(call, "{{") {
			t.Fatalf("a placeholder was typed literally: %v", fake.calls)
		}
	}
}

// End to end against a real headless browser and a local form.
func TestBrowserStepsFillARealForm(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := browser.FindExecutable(); err != nil {
		t.Skip("no Chromium-based browser installed")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			fmt.Fprintf(w, "<html><body><h1 id=done>Thanks %s</h1></body></html>", r.FormValue("name"))
			return
		}
		fmt.Fprint(w, `<html><body><form method=post><input id=name name=name><button id=go type=submit>Send</button></form></body></html>`)
	}))
	defer server.Close()
	t.Cleanup(func() { _ = browser.Close() })

	env := &Env{Workspace: t.TempDir()}
	result, _ := (&browserRunStepsTool{}).Run(context.Background(), env, map[string]any{
		"steps": []any{
			map[string]any{"action": "navigate", "url": server.URL},
			map[string]any{"action": "type", "selector": "#name", "value": "{{who}}"},
			map[string]any{"action": "click", "selector": "#go"},
			map[string]any{"action": "wait", "selector": "#done"},
			map[string]any{"action": "read", "selector": "#done"},
		},
		"vars": map[string]any{"who": "Termixgo"},
	})
	if result.IsError || !strings.Contains(result.Output, "Thanks Termixgo") {
		t.Fatalf("result = %+v", result)
	}
}
