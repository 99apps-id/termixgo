package mcp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain turns the test binary into the fake server when the helper variable
// is set, which is what lets the client be tested against a real process
// speaking a real protocol on a real pipe.
func TestMain(m *testing.M) {
	if runHelperIfRequested() {
		return
	}
	os.Exit(m.Run())
}

// startFake starts the fake server in a mode and returns a connected client.
func startFake(t *testing.T, mode string) *Client {
	t.Helper()
	client, err := Start(t.Context(), fakeOptions(mode))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

// fakeOptions describes the fake server re-executed as the test binary.
func fakeOptions(mode string) Options {
	return Options{
		Name:    "fake",
		Command: os.Args[0],
		Args:    []string{"-test.run=^$"},
		Env:     map[string]string{helperEnv: "1", modeEnv: mode},
	}
}

// TestStartCompletesTheHandshake is the first thing every other test depends
// on: the client negotiates a revision and learns the server's own name.
func TestStartCompletesTheHandshake(t *testing.T) {
	client := startFake(t, modeNormal)

	if client.Name() != "fake" {
		t.Errorf("Name = %q, want the configured label", client.Name())
	}
	if client.ServerName() != "fake-server" {
		t.Errorf("ServerName = %q, want the name the server reported", client.ServerName())
	}
	if client.ProtocolVersion() != ProtocolVersion {
		t.Errorf("ProtocolVersion = %q, want %q", client.ProtocolVersion(), ProtocolVersion)
	}
}

// TestStartRefusesAnEmptyCommand keeps a config typo from producing a process
// that cannot exist.
func TestStartRefusesAnEmptyCommand(t *testing.T) {
	if _, err := Start(t.Context(), Options{Name: "blank"}); err == nil {
		t.Fatalf("a server with no command must be refused")
	}
}

// TestToolsListsAndCaches covers the listing and the cache, which is what stops
// a reload from asking the same server twice for a list it cannot change.
func TestToolsListsAndCaches(t *testing.T) {
	client := startFake(t, modeNormal)

	tools, err := client.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("Tools returned %d tools, want 2", len(tools))
	}
	if tools[0].Name != "echo" || tools[1].Name != "write_it" {
		t.Errorf("tools = %q, want echo then write_it", []string{tools[0].Name, tools[1].Name})
	}
	if tools[0].Annotations == nil || !tools[0].Annotations.ReadOnlyHint {
		t.Errorf("the read-only hint was not decoded: %+v", tools[0].Annotations)
	}
	if len(tools[0].InputSchema) == 0 {
		t.Errorf("the input schema was not decoded")
	}

	// The second read comes from the cache and must agree.
	again, err := client.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools (cached): %v", err)
	}
	if len(again) != 2 || again[0].Name != "echo" {
		t.Errorf("the cached listing differs: %+v", again)
	}
}

// TestToolsFollowsPagination is the correctness rule for a server with more
// tools than fit in one answer: a client that ignores the cursor silently loses
// most of a large server.
func TestToolsFollowsPagination(t *testing.T) {
	client := startFake(t, modePaginated)

	tools, err := client.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "first,second" {
		t.Errorf("tools = %v, want both pages", names)
	}
}

// TestToolsOnAnEmptyServerIsNotAnError keeps a healthy server that publishes
// nothing from looking like a broken one.
func TestToolsOnAnEmptyServerIsNotAnError(t *testing.T) {
	client := startFake(t, modeNoTools)

	tools, err := client.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("Tools = %+v, want nothing", tools)
	}
}

// TestCallReturnsTheToolText runs one tool end to end.
func TestCallReturnsTheToolText(t *testing.T) {
	client := startFake(t, modeNormal)

	result, err := client.Call(context.Background(), "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result.IsError {
		t.Errorf("IsError = true, want false")
	}
	if got := result.Text(); got != "echo: hello" {
		t.Errorf("Text = %q, want the echoed text", got)
	}
}

// TestCallPassesNoArgumentsAsAnEmptyObject keeps a tool with no parameters from
// receiving a null arguments field, which some servers reject outright.
func TestCallPassesNoArgumentsAsAnEmptyObject(t *testing.T) {
	client := startFake(t, modeNormal)

	if _, err := client.Call(context.Background(), "write_it", nil); err != nil {
		t.Fatalf("Call with no arguments: %v", err)
	}
}

// TestCallReportsAToolFailureSeparately is the distinction that matters to the
// model: a tool that ran and failed is not a protocol that broke.
func TestCallReportsAToolFailureSeparately(t *testing.T) {
	client := startFake(t, modeServerError)

	result, err := client.Call(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("a tool that reported its own failure must not be an error: %v", err)
	}
	if !result.IsError {
		t.Errorf("IsError = false, want true")
	}
	if !strings.Contains(result.Text(), "unreachable") {
		t.Errorf("Text = %q, want the tool's own message", result.Text())
	}
}

// TestCallSurfacesAProtocolError is the other side: a JSON-RPC error is the
// server refusing the call, and the message it sent is what the operator needs.
func TestCallSurfacesAProtocolError(t *testing.T) {
	client := startFake(t, modeCallError)

	_, err := client.Call(context.Background(), "echo", nil)
	if err == nil {
		t.Fatalf("a JSON-RPC error must be reported")
	}
	if !strings.Contains(err.Error(), "refused to run") {
		t.Errorf("err = %v, want the server's message", err)
	}
}

// TestNotificationsDoNotConfuseTheResponse covers the interleaving every real
// server does: a message with no id arrives while a request is outstanding.
func TestNotificationsDoNotConfuseTheResponse(t *testing.T) {
	client := startFake(t, modeNotification)

	tools, err := client.Tools(context.Background())
	if err != nil {
		t.Fatalf("a notification before the answer must be tolerated: %v", err)
	}
	if len(tools) != 2 {
		t.Errorf("Tools returned %d tools, want 2", len(tools))
	}
}

// TestNonJSONOnStdoutIsReported is the failure a server author makes by printing
// a banner. It poisons the stream, so it has to be named rather than skipped.
func TestNonJSONOnStdoutIsReported(t *testing.T) {
	client := startFake(t, modeProse)

	_, err := client.Tools(context.Background())
	if err == nil {
		t.Fatalf("a non-JSON line on stdout must be reported")
	}
	if !strings.Contains(err.Error(), "non-JSON") {
		t.Errorf("err = %v, want it to name the poisoned stream", err)
	}
}

// TestCrashReportsTheServerStderr is the diagnosis path: the operator needs the
// server's own explanation, not a broken pipe.
func TestCrashReportsTheServerStderr(t *testing.T) {
	_, err := Start(t.Context(), fakeOptions(modeCrash))
	if err == nil {
		t.Fatalf("a server that exits during the handshake must fail")
	}
	if !strings.Contains(err.Error(), "cannot find module") {
		t.Errorf("err = %v, want the server's stderr in it", err)
	}
}

// TestSilentServerIsBoundedByTheContext is the anti-hang rule: a server that
// takes the handshake and then never answers must not wedge the caller.
func TestSilentServerIsBoundedByTheContext(t *testing.T) {
	client := startFake(t, modeSilent)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Call(ctx, "echo", nil); err == nil {
		t.Fatalf("a call past its deadline must fail")
	}
}

// TestCloseIsIdempotent keeps a second close on shutdown from panicking, which
// matters because the app closes the pool on a signal path that can run twice.
func TestCloseIsIdempotent(t *testing.T) {
	client, err := Start(t.Context(), fakeOptions(modeNormal))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	client.Close()
	client.Close()

	if _, err := client.Call(context.Background(), "echo", nil); err == nil {
		t.Errorf("a closed client must refuse a call")
	}
}

// TestResultTextDescribesNonTextBlocks keeps an image result from looking like
// an empty one, which would tell the model the tool returned nothing.
func TestResultTextDescribesNonTextBlocks(t *testing.T) {
	client := startFake(t, modeNormal)

	result, err := client.Call(context.Background(), "image_only", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	text := result.Text()
	if !strings.Contains(text, "image") || !strings.Contains(text, "image/png") {
		t.Errorf("Text = %q, want the block type and mime type", text)
	}
}

// TestEmptyResultSaysSo pins the same rule for a genuinely empty result.
func TestEmptyResultSaysSo(t *testing.T) {
	client := startFake(t, modeNormal)

	result, err := client.Call(context.Background(), "empty", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result.Text() != "(no output)" {
		t.Errorf("Text = %q, want the explicit empty marker", result.Text())
	}
}
