package mcp

import (
	"context"
	"strings"
	"testing"
)

// A server that crashes mid-call is restarted: the interrupted call reports an
// unknown outcome instead of being repeated, and the next call works.
func TestPoolRestartsACrashedServer(t *testing.T) {
	pool := connectFake(t, modeNormal)

	// "die" is not a listed tool, so it is sent through the client directly
	// under the bound echo name's server, the way a crashing tool would be.
	pool.mu.Lock()
	client := pool.clients["fake"]
	pool.mu.Unlock()
	if client == nil {
		t.Fatalf("the fake server should be connected")
	}
	if _, err := client.Call(context.Background(), "die", nil); err == nil {
		t.Fatalf("a crashed call should fail")
	}
	if !client.Dead() {
		t.Fatalf("the client should report the crash")
	}

	result, err := pool.Call(context.Background(), "mcp_fake__echo", map[string]any{"text": "back"})
	if err != nil {
		t.Fatalf("the pool should restart the server and answer: %v", err)
	}
	if result.Text() != "echo: back" {
		t.Fatalf("Text = %q", result.Text())
	}
}

// A crash during a pool call restarts the server and tells the model the
// outcome is unknown, without running the call a second time.
func TestPoolDoesNotRepeatACallThatCrashed(t *testing.T) {
	pool := connectFake(t, modeNormal)
	pool.mu.Lock()
	pool.bound = append(pool.bound, Bound{Server: "fake", Tool: Tool{Name: "die"}, Name: "mcp_fake__die"})
	pool.mu.Unlock()

	_, err := pool.Call(context.Background(), "mcp_fake__die", nil)
	if err == nil || !strings.Contains(err.Error(), "restarted") {
		t.Fatalf("the error should say the server was restarted, got %v", err)
	}
	if _, err := pool.Call(context.Background(), "mcp_fake__echo", map[string]any{"text": "ok"}); err != nil {
		t.Fatalf("the restarted server should answer: %v", err)
	}
}
