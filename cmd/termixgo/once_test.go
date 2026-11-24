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
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

// localEndpoint configures the throwaway state so the default model is a local
// one pointed at a test server, which is what lets a real turn run without a
// key and without the network.
func localEndpoint(t *testing.T, home string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"hello from the model\"}}]}\n\n")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))

	cfg := config.Default()
	cfg.DefaultModel = "qwen2.5-coder:latest"
	cfg.ApprovalMode = config.ApprovalAll
	cfg.BaseURLs = map[string]string{"ollama": server.URL}
	if err := config.Save(cfg); err != nil {
		server.Close()
		t.Fatalf("save config: %v", err)
	}
	return server
}

// TestRunOnceCompletesATurnAndLeavesNoSession covers the whole `-p` path: the
// app is built, the turn runs, the answer reaches stdout, and no conversation
// file is left behind for the next invocation to trip over.
func TestRunOnceCompletesATurnAndLeavesNoSession(t *testing.T) {
	home := withState(t)
	server := localEndpoint(t, home)
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if err := runOnce(runOptions{prompt: "say hi"}, &stdout, &stderr); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if !strings.Contains(stdout.String(), "hello from the model") {
		t.Errorf("stdout = %q, want the answer", stdout.String())
	}

	// A one-shot run is a command, not a conversation.
	entries, err := os.ReadDir(filepath.Join(home, "sessions"))
	if err == nil && len(entries) != 0 {
		t.Errorf("a one-shot run wrote %d session file(s)", len(entries))
	}
}

// TestRunOnceReportsATurnFailure keeps a provider error from looking like a
// successful run with no output.
func TestRunOnceReportsATurnFailure(t *testing.T) {
	withState(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(writer, `{"error":{"message":"bad key","type":"auth"}}`)
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.DefaultModel = "qwen2.5-coder:latest"
	cfg.BaseURLs = map[string]string{"ollama": server.URL}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := runOnce(runOptions{prompt: "say hi"}, &stdout, &stderr); err == nil {
		t.Fatalf("a rejected request must fail the command")
	}
}

// TestParseRunArgsHandlesYesInEveryPosition pins the flag placement: a flag may
// lead or trail the prompt, and prompt words are never eaten by the flag scan.
func TestParseRunArgsHandlesYesInEveryPosition(t *testing.T) {
	cases := []struct {
		args   []string
		prompt string
		yes    bool
		json   bool
	}{
		{[]string{"--yes", "fix", "the", "test"}, "fix the test", true, false},
		{[]string{"-y", "fix the test"}, "fix the test", true, false},
		{[]string{"fix the test", "--yes"}, "fix the test", true, false},
		{[]string{"fix the test"}, "fix the test", false, false},
		{[]string{"answer", "--yes", "me"}, "answer me", true, false},
		{[]string{"--json", "--yes", "code it"}, "code it", true, true},
		{[]string{"inspect", "--json"}, "inspect", false, true},
	}
	for _, c := range cases {
		opts, err := parseRunArgs(c.args)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if opts.prompt != c.prompt || opts.autoApprove != c.yes || opts.json != c.json {
			t.Errorf("%v = (%q, %v, %v), want (%q, %v, %v)", c.args, opts.prompt, opts.autoApprove, opts.json, c.prompt, c.yes, c.json)
		}
	}
	if _, err := parseRunArgs([]string{"--yes"}); err == nil {
		t.Errorf("a --yes with no prompt must fail")
	}
}

func TestParseRunArgsReadsTheValueFlags(t *testing.T) {
	opts, err := parseRunArgs([]string{"-m", "gpt-x", "--agent", "docs", "--effort", "high", "-f", "a.png", "--file", "b.pdf", "--title", "Nightly", "explain", "this", "-c"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.model != "gpt-x" || opts.agent != "docs" || opts.effort != "high" || opts.title != "Nightly" || !opts.continueLast {
		t.Fatalf("opts = %+v", opts)
	}
	if len(opts.files) != 2 || opts.files[0] != "a.png" || opts.files[1] != "b.pdf" || opts.prompt != "explain this" {
		t.Fatalf("files=%v prompt=%q", opts.files, opts.prompt)
	}
	if !opts.keepsSession() {
		t.Errorf("a titled or continued run keeps its session")
	}
	plain, _ := parseRunArgs([]string{"-m", "x", "hi"})
	if plain.keepsSession() {
		t.Errorf("a model choice alone must not keep a session")
	}
	for _, bad := range [][]string{{"hi", "--model"}, {"hi", "-f"}, {"hi", "--title"}, {"-c", "--session", "x", "hi"}} {
		if _, err := parseRunArgs(bad); err == nil {
			t.Errorf("%v should be refused", bad)
		}
	}
}

// TestDoctorOnAFreshInstall covers the state a new user is actually in: no
// secret file at all. That must read as a fact, not as a failure.
func TestDoctorOnAFreshInstall(t *testing.T) {
	withState(t)

	stdout, _, err := runCLI(t, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, "no secret file yet") {
		t.Errorf("doctor output = %q, want it to say there is no secret file", stdout)
	}
	if strings.Contains(stdout, "WARNING") {
		t.Errorf("nothing is exposed yet, so there should be no warning:\n%s", stdout)
	}
}

// TestDoctorNamesProvidersWithoutKeys is the onboarding hint: the operator
// needs to know which providers are ready and which still need a key.
func TestDoctorNamesProvidersWithoutKeys(t *testing.T) {
	withState(t)

	stdout, _, err := runCLI(t, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, "providers:") {
		t.Errorf("output = %q, want the provider section", stdout)
	}
	// A local provider needs no key and must not be reported as unconfigured
	// in a way that suggests a problem.
	if !strings.Contains(stdout, "ollama") {
		t.Errorf("output = %q, want the local provider listed", stdout)
	}
}

// TestShortCommandAliasesWork pins the two spellings operators type by habit.
func TestShortCommandAliasesWork(t *testing.T) {
	home := withState(t)
	server := localEndpoint(t, home)
	defer server.Close()

	for _, alias := range []string{"-p", "--print"} {
		var stdout, stderr bytes.Buffer
		if err := run([]string{alias, "answer me"}, strings.NewReader(""), &stdout, &stderr); err != nil {
			t.Fatalf("%s: %v", alias, err)
		}
		if !strings.Contains(stdout.String(), "hello from the model") {
			t.Errorf("%s stdout = %q", alias, stdout.String())
		}
	}
}

// TestRunJoinsAMultiWordPrompt guards the quoting-free usage: the words after
// the subcommand are one prompt, not separate arguments.
func TestRunJoinsAMultiWordPrompt(t *testing.T) {
	var seen string
	withState(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		seen = string(body)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.DefaultModel = "qwen2.5-coder:latest"
	cfg.BaseURLs = map[string]string{"ollama": server.URL}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"run", "fix", "the", "flaky", "test"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(seen, "fix the flaky test") {
		t.Errorf("the request body should carry the joined prompt, got %q", seen)
	}
}
