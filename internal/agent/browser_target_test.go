package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fileURL(path string) string {
	slashed := filepath.ToSlash(path)
	if runtime.GOOS == "windows" {
		return "file:///" + slashed
	}
	return "file://" + slashed
}

// A live browser session must obey the same workspace boundary as every file
// tool: navigating to a file URL and reading the page is a file read.
func TestBrowserNavigateRefusesAFileOutsideTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := &Env{Workspace: workspace}
	result, _ := (&browserNavigateTool{}).Run(context.Background(), env, map[string]any{"url": fileURL(outside)})
	if !result.IsError || !strings.Contains(result.Output, "outside workspace") {
		t.Fatalf("navigation to a file outside the workspace was not refused: %+v", result)
	}
	tab, _ := (&browserTabsTool{}).Run(context.Background(), env, map[string]any{"action": "new", "url": fileURL(outside)})
	if !tab.IsError || !strings.Contains(tab.Output, "outside workspace") {
		t.Fatalf("a new tab on a file outside the workspace was not refused: %+v", tab)
	}
}

func TestCheckBrowserTarget(t *testing.T) {
	workspace := t.TempDir()
	inside := filepath.Join(workspace, "page.html")
	if err := os.WriteFile(inside, []byte("<p>x</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := &Env{Workspace: workspace}
	for _, ok := range []string{"https://example.com", "http://localhost:3000", "about:blank", "data:text/html,hi", fileURL(inside)} {
		if err := checkBrowserTarget(env, ok); err != nil {
			t.Errorf("%q should pass: %v", ok, err)
		}
	}
	for _, bad := range []string{"file://remote-host/share/a.html"} {
		if err := checkBrowserTarget(env, bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	guest := &Env{Workspace: workspace, Guest: true}
	if err := os.WriteFile(filepath.Join(workspace, ".env"), []byte("A=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkBrowserTarget(guest, fileURL(filepath.Join(workspace, ".env"))); err == nil {
		t.Errorf("a guest must not open .env through the browser")
	}
}
