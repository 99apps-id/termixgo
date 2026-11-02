package browser

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const endToEndPage = `<!doctype html>
<html>
<head><title>Termixgo Browser Test</title></head>
<body>
  <h1>Hello</h1>
  <button id="go" onclick="document.getElementById('out').textContent = 'clicked'">Go</button>
  <div id="out">idle</div>
  <input id="name" placeholder="name" />
  <ul><li>First</li><li>Second</li></ul>
</body>
</html>`

// TestLiveBrowserEndToEnd drives a real headless browser against a local page.
// It skips when no Chromium is installed or the host refuses to start one, so
// the test is a smoke test on a developer machine rather than a CI gate.
func TestLiveBrowserEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the live browser test in short mode")
	}
	if _, err := FindExecutable(); err != nil {
		t.Skipf("no Chromium available: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, endToEndPage)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	session, err := Open(ctx)
	if err != nil {
		t.Skipf("cannot start a browser here: %v", err)
	}
	defer func() { _ = Close() }()

	summary, err := session.Navigate(ctx, server.URL)
	if err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if !strings.Contains(summary, "Termixgo Browser Test") {
		t.Fatalf("summary = %q", summary)
	}

	text, err := session.Read(ctx, "text", "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{"Hello", "idle", "First"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text %q missing %q", text, want)
		}
	}

	if err := session.Click(ctx, "#go", "", 0, 0); err != nil {
		t.Fatalf("click: %v", err)
	}
	if err := session.WaitFor(ctx, "", "clicked", "", 5*time.Second); err != nil {
		t.Fatalf("wait: %v", err)
	}
	text, err = session.Read(ctx, "text", "#out")
	if err != nil {
		t.Fatalf("read after click: %v", err)
	}
	if strings.TrimSpace(text) != "clicked" {
		t.Fatalf("clicked text = %q", text)
	}

	if err := session.Type(ctx, "#name", "Ada Lovelace", true, false); err != nil {
		t.Fatalf("type: %v", err)
	}
	value, err := session.Evaluate(ctx, `document.querySelector('#name').value`)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if value != "Ada Lovelace" {
		t.Fatalf("input value = %v", value)
	}

	forms, err := session.Read(ctx, "forms", "")
	if err != nil {
		t.Fatalf("read forms: %v", err)
	}
	if !strings.Contains(forms, "#name") || !strings.Contains(forms, "#go") {
		t.Fatalf("forms = %q", forms)
	}

	if err := session.Scroll(ctx, "down", 2); err != nil {
		t.Fatalf("scroll: %v", err)
	}

	for _, test := range []struct {
		name     string
		fullPage bool
		selector string
	}{
		{name: "viewport"},
		{name: "full page", fullPage: true},
		{name: "element", selector: "#go"},
	} {
		data, err := session.Screenshot(ctx, test.fullPage, test.selector)
		if err != nil {
			t.Fatalf("screenshot %s: %v", test.name, err)
		}
		if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatalf("screenshot %s is not a PNG (%d bytes)", test.name, len(data))
		}
	}

	if err := session.SetCookie(ctx, "session", "abc", server.URL); err != nil {
		t.Fatalf("set cookie: %v", err)
	}
	cookies, err := session.Cookies(ctx)
	if err != nil {
		t.Fatalf("cookies: %v", err)
	}
	found := false
	for _, cookie := range cookies {
		if cookie.Name == "session" && cookie.Value == "abc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cookie not set: %+v", cookies)
	}
	if err := session.ClearCookies(ctx); err != nil {
		t.Fatalf("clear cookies: %v", err)
	}

	tabs, err := session.ListTabs(ctx)
	if err != nil || len(tabs) == 0 {
		t.Fatalf("tabs: %v %+v", err, tabs)
	}
	original := tabs[0].ID
	created, err := session.NewTab(ctx, "about:blank")
	if err != nil {
		t.Fatalf("new tab: %v", err)
	}
	if _, err := session.SelectTab(ctx, original); err != nil {
		t.Fatalf("select tab: %v", err)
	}
	if err := session.CloseTab(ctx, created.ID); err != nil {
		t.Fatalf("close tab: %v", err)
	}
}
