package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSlashEndpointAddFetchesModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen3-coder"}]}`))
	}))
	defer server.Close()

	model := chatModel(t)
	next, cmd := model.runSlash("endpoint", "add lab "+server.URL+"/v1 sk-hidden")
	updated := next.(*Model)
	if cmd == nil {
		t.Fatalf("adding an endpoint should fetch its models")
	}
	if view := display(updated); strings.Contains(view, "sk-hidden") {
		t.Fatalf("the key must never be shown:\n%s", view)
	}
	msg := cmd()
	refreshed, _ := updated.Update(msg)
	view := display(refreshed.(*Model))
	if !strings.Contains(view, "openai-compatible/lab serves 1 model(s): qwen3-coder") {
		t.Fatalf("the models should be reported:\n%s", view)
	}

	listed, _ := refreshed.(*Model).runSlash("endpoint", "")
	if view := display(listed.(*Model)); !strings.Contains(view, "openai-compatible/lab") || !strings.Contains(view, "key set") {
		t.Fatalf("list should show the endpoint and its key state:\n%s", view)
	}
}
