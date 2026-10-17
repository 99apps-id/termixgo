package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserSessionLifecycle(t *testing.T) {
	mockHTML := `<!DOCTYPE html>
<html>
<head><title>99apps Test Dashboard</title></head>
<body>
  <h1>Welcome to 99apps</h1>
  <form id="claim-form">
    <input type="text" name="subdomain" id="subdomain-input" placeholder="Enter subdomain" />
    <button type="submit" id="submit-btn">Check and Claim</button>
  </form>
</body>
</html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(mockHTML))
	}))
	defer server.Close()

	ctx := context.Background()
	env := testEnv(t)

	openTool := &browserSessionOpenTool{}
	resOpen, err := openTool.Run(ctx, env, map[string]any{"url": server.URL})
	if err != nil || resOpen.IsError {
		t.Fatalf("browser_session_open failed: err=%v, res=%+v", err, resOpen)
	}
	if !strings.Contains(resOpen.Output, "99apps Test Dashboard") {
		t.Errorf("open output missing title, got %q", resOpen.Output)
	}

	readTool := &browserSessionReadTool{}
	resRead, err := readTool.Run(ctx, env, map[string]any{"include_elements": true})
	if err != nil || resRead.IsError {
		t.Fatalf("browser_session_read failed: err=%v, res=%+v", err, resRead)
	}
	if !strings.Contains(resRead.Output, "subdomain-input") || !strings.Contains(resRead.Output, "submit-btn") {
		t.Errorf("read did not extract form elements, got %q", resRead.Output)
	}

	closeTool := &browserSessionCloseTool{}
	resClose, err := closeTool.Run(ctx, env, map[string]any{})
	if err != nil || resClose.IsError {
		t.Fatalf("browser_session_close failed: err=%v, res=%+v", err, resClose)
	}
	if !strings.Contains(resClose.Output, "Closed browser session") {
		t.Errorf("close unexpected output: %q", resClose.Output)
	}

	resReread, _ := readTool.Run(ctx, env, map[string]any{})
	if !resReread.IsError {
		t.Errorf("read after close should fail, got %q", resReread.Output)
	}
}

func TestBrowserSessionOpenValidation(t *testing.T) {
	env := testEnv(t)
	tool := &browserSessionOpenTool{}

	res, _ := tool.Run(context.Background(), env, map[string]any{"url": ""})
	if !res.IsError {
		t.Fatalf("expected error for empty url")
	}
	res, _ = tool.Run(context.Background(), env, map[string]any{"url": "http://169.254.169.254/latest/meta-data/"})
	if !res.IsError {
		t.Fatalf("expected error for a metadata address, got %q", res.Output)
	}
}
