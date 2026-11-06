package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeURLSuccessAndFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if !probeURL(srv.URL, 2*time.Second) {
		t.Fatalf("expected probeURL true for %s", srv.URL)
	}
	if probeURL("http://127.0.0.1:1", 300*time.Millisecond) {
		t.Fatal("expected probeURL false for a closed port")
	}
}

func TestDetectDevCommand(t *testing.T) {
	dev := t.TempDir()
	if err := os.WriteFile(filepath.Join(dev, "package.json"), []byte(`{"scripts":{"dev":"vite"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectDevCommand(dev); got != "npm run dev" {
		t.Fatalf("dev script: got %q, want %q", got, "npm run dev")
	}

	start := t.TempDir()
	if err := os.WriteFile(filepath.Join(start, "package.json"), []byte(`{"scripts":{"start":"node server.js"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectDevCommand(start); got != "npm start" {
		t.Fatalf("start script: got %q, want %q", got, "npm start")
	}

	if got := detectDevCommand(t.TempDir()); got != "" {
		t.Fatalf("no package.json: got %q, want empty", got)
	}
}
