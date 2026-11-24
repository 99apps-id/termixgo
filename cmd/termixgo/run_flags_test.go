package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

// recordingEndpoint is a local model server that keeps every request body, so
// a test can see what a run actually sent.
type recordingEndpoint struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newRecordingEndpoint(t *testing.T) *recordingEndpoint {
	t.Helper()
	endpoint := &recordingEndpoint{}
	endpoint.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		endpoint.mu.Lock()
		endpoint.bodies = append(endpoint.bodies, string(body))
		endpoint.mu.Unlock()
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	t.Cleanup(endpoint.Close)
	cfg := config.Default()
	cfg.DefaultModel = "qwen2.5-coder:latest"
	cfg.ApprovalMode = config.ApprovalAll
	cfg.BaseURLs = map[string]string{"ollama": endpoint.URL}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func (e *recordingEndpoint) requests() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.bodies...)
}

func runWith(t *testing.T, args ...string) error {
	t.Helper()
	opts, err := parseRunArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	return runOnce(opts, &stdout, &stderr)
}

func sessionFiles(t *testing.T, home string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(home, "sessions"))
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	return names
}

// --model is for one run: the request carries it, the saved default does not
// change, and the run leaves no session behind.
func TestRunModelFlagDoesNotChangeTheSavedDefault(t *testing.T) {
	home := withState(t)
	endpoint := newRecordingEndpoint(t)

	if err := runWith(t, "-m", "my-local-model", "--effort", "high", "say hi"); err != nil {
		t.Fatal(err)
	}
	requests := endpoint.requests()
	if len(requests) == 0 || !strings.Contains(requests[0], "my-local-model") {
		t.Fatalf("the request did not use the chosen model: %v", requests)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "qwen2.5-coder:latest" || cfg.Effort != "" {
		t.Fatalf("a one-shot flag was saved: model=%q effort=%q", cfg.DefaultModel, cfg.Effort)
	}
	if files := sessionFiles(t, home); len(files) != 0 {
		t.Fatalf("a run with no session flag wrote %v", files)
	}
}

func TestRunRefusesAnUnknownEffortAndAgent(t *testing.T) {
	withState(t)
	endpoint := newRecordingEndpoint(t)
	if err := runWith(t, "--effort", "warp", "hi"); err == nil {
		t.Errorf("an effort level the build does not know must fail")
	}
	if err := runWith(t, "--agent", "nobody", "hi"); err == nil {
		t.Errorf("an unknown agent must fail")
	}
	if got := len(endpoint.requests()); got != 0 {
		t.Errorf("a refused flag still sent %d request(s)", got)
	}
}

// --title keeps the run as a named session and -c adds the next run to it.
func TestRunTitleAndContinueKeepOneConversation(t *testing.T) {
	home := withState(t)
	endpoint := newRecordingEndpoint(t)

	if err := runWith(t, "--title", "Nightly check", "first question"); err != nil {
		t.Fatal(err)
	}
	files := sessionFiles(t, home)
	if len(files) != 1 {
		t.Fatalf("a titled run kept %d session file(s), want 1", len(files))
	}
	saved, _ := os.ReadFile(filepath.Join(home, "sessions", files[0]))
	if !strings.Contains(string(saved), "Nightly check") {
		t.Fatalf("the title was not saved: %s", saved)
	}

	if err := runWith(t, "-c", "second question"); err != nil {
		t.Fatal(err)
	}
	if again := sessionFiles(t, home); len(again) != 1 {
		t.Fatalf("continue opened a new session: %v", again)
	}
	requests := endpoint.requests()
	if last := requests[len(requests)-1]; !strings.Contains(last, "first question") || !strings.Contains(last, "second question") {
		t.Fatalf("the continued run did not carry the earlier turn: %s", last)
	}
}

func TestRunContinueWithNothingToContinueFails(t *testing.T) {
	withState(t)
	newRecordingEndpoint(t)
	if err := runWith(t, "-c", "hi"); err == nil {
		t.Errorf("continuing with no saved session must fail, not start a new one silently")
	}
}

// --file puts a document into the prompt, and an unreadable file stops the run
// before the provider is called.
func TestRunFileFlagAttachesTheDocument(t *testing.T) {
	withState(t)
	endpoint := newRecordingEndpoint(t)
	if err := os.WriteFile("notes.txt", []byte("ZEBRA-42 is the answer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runWith(t, "-f", "notes.txt", "what does the file say"); err != nil {
		t.Fatal(err)
	}
	requests := endpoint.requests()
	if len(requests) == 0 || !strings.Contains(requests[0], "ZEBRA-42") {
		t.Fatalf("the document did not reach the model: %v", requests)
	}

	before := len(endpoint.requests())
	if err := runWith(t, "-f", "missing.txt", "hi"); err == nil {
		t.Fatalf("a missing file must fail the run")
	}
	if len(endpoint.requests()) != before {
		t.Errorf("a missing file still reached the provider")
	}
}
