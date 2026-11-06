package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeHTTPState records what the client sent so the test can assert the session
// header and the bearer header actually travelled.
type fakeHTTPState struct {
	mu          sync.Mutex
	initializes int
	sessionSeen []string
	authSeen    string
}

func (s *fakeHTTPState) noteSession(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionSeen = append(s.sessionSeen, value)
}

func (s *fakeHTTPState) counts() (int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initializes, append([]string(nil), s.sessionSeen...)
}

func TestHTTPTransportEndToEnd(t *testing.T) {
	state := &fakeHTTPState{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		state.mu.Lock()
		state.authSeen = request.Header.Get("Authorization")
		state.mu.Unlock()
		if request.Method == http.MethodDelete {
			writer.WriteHeader(http.StatusOK)
			return
		}
		body, _ := io.ReadAll(request.Body)
		var call struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(body, &call); err != nil {
			http.Error(writer, "bad json", http.StatusBadRequest)
			return
		}
		if call.Method == "initialize" {
			state.mu.Lock()
			state.initializes++
			number := state.initializes
			state.mu.Unlock()
			writer.Header().Set("Mcp-Session-Id", fmt.Sprintf("session-%d", number))
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"fake","version":"1"}}}`, string(call.ID))
			return
		}
		state.noteSession(request.Header.Get("Mcp-Session-Id"))
		switch call.Method {
		case "notifications/initialized":
			writer.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "event: message\n")
			fmt.Fprint(writer, `data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`+"\n\n")
			fmt.Fprint(writer, "event: message\n")
			fmt.Fprintf(writer, `data: {"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo","description":"Echo a value"}]}}`+"\n\n", string(call.ID))
		case "tools/call":
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"hello"}]}}`, string(call.ID))
		default:
			http.Error(writer, "unknown method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	client, err := Start(ctx, Options{
		Name:    "remote",
		URL:     server.URL,
		Headers: map[string]string{"Authorization": "Bearer token"},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()

	if client.ServerName() != "fake" || client.ProtocolVersion() != ProtocolVersion {
		t.Fatalf("handshake: name=%q version=%q", client.ServerName(), client.ProtocolVersion())
	}

	tools, err := client.Tools(ctx)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}

	result, err := client.Call(ctx, "echo", map[string]any{"value": "x"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if result.Text() != "hello" {
		t.Fatalf("result = %q", result.Text())
	}

	initializeCount, sessions := state.counts()
	if initializeCount != 1 {
		t.Fatalf("initialize count = %d", initializeCount)
	}
	for _, session := range sessions {
		if session != "session-1" {
			t.Fatalf("a request travelled without the session header: %q in %v", session, sessions)
		}
	}
	state.mu.Lock()
	auth := state.authSeen
	state.mu.Unlock()
	if auth != "Bearer token" {
		t.Fatalf("authorization header = %q", auth)
	}
}

func TestHTTPTransportReinitializesOnExpiredSession(t *testing.T) {
	state := &fakeHTTPState{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var call struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &call)
		if request.Method == http.MethodDelete {
			writer.WriteHeader(http.StatusOK)
			return
		}
		if call.Method == "initialize" {
			state.mu.Lock()
			state.initializes++
			number := state.initializes
			state.mu.Unlock()
			writer.Header().Set("Mcp-Session-Id", fmt.Sprintf("session-%d", number))
			fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"fake"}}}`, string(call.ID))
			return
		}
		if call.Method == "notifications/initialized" {
			writer.WriteHeader(http.StatusAccepted)
			return
		}
		if request.Header.Get("Mcp-Session-Id") == "session-1" {
			// The server forgot the first session, which the transport reports
			// as 404 and the client must recover from exactly once.
			http.Error(writer, "session expired", http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo"}]}}`, string(call.ID))
	}))
	defer server.Close()

	client, err := Start(context.Background(), Options{Name: "remote", URL: server.URL})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()
	tools, err := client.Tools(context.Background())
	if err != nil {
		t.Fatalf("tools after recovery: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}
	if count, _ := state.counts(); count != 2 {
		t.Fatalf("initialize count = %d, want a second handshake", count)
	}
}

func TestIsHTTPTransport(t *testing.T) {
	if !isHTTP(Options{URL: "https://example.com/mcp"}) {
		t.Fatal("a URL with no command means HTTP")
	}
	if isHTTP(Options{Command: "npx", URL: "https://example.com/mcp", Transport: "stdio"}) {
		t.Fatal("an explicit stdio transport must win over a URL")
	}
	if !isHTTP(Options{Command: "npx", Transport: "http", URL: "https://example.com/mcp"}) {
		t.Fatal("an explicit http transport must win over a command")
	}
	if isHTTP(Options{Command: "npx"}) {
		t.Fatal("a command with no URL is stdio")
	}
}

func TestHTTPTransportRejectsBadURL(t *testing.T) {
	for _, target := range []string{"", "example.com/mcp", "ftp://example.com/mcp"} {
		if _, err := Start(context.Background(), Options{Name: "bad", URL: target, Transport: "http"}); err == nil {
			t.Fatalf("%q should be refused", target)
		}
	}
}

func TestHTTPTransportReportsStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()
	_, err := Start(context.Background(), Options{Name: "broken", URL: server.URL})
	if err == nil {
		t.Fatal("a 500 must fail the handshake")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error should name the status: %v", err)
	}
}
