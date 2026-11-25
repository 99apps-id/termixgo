package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMuseCapturesMuseCLIHeaders records the outbound request shape on a
// local mock the same way a packet capture of the official CLI would: the
// User-Agent, the tbh identity headers, and the tools field.
func TestMuseCapturesMuseCLIHeaders(t *testing.T) {
	var ua, clientID, agentRole, threadID, sessionID, gatewaySession, rootSession string
	var path string
	var tools []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		ua = r.Header.Get("User-Agent")
		clientID = r.Header.Get("x-client-id")
		agentRole = r.Header.Get("x-tbh-agent-role")
		threadID = r.Header.Get("x-tbh-thread-id")
		sessionID = r.Header.Get("x-tbh-session-id")
		gatewaySession = r.Header.Get("x-meta-ai-gateway-session-id")
		rootSession = r.Header.Get("x-tbh-root-session-id")
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if rawTools, ok := body["tools"].([]any); ok {
			for _, entry := range rawTools {
				if typed, ok := entry.(map[string]any); ok {
					tools = append(tools, typed)
					break
				}
			}
		}
		if err := r.Body.Close(); err != nil {
			t.Errorf("close body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "muse", Label: "Meta Muse Code", Kind: KindMuse}, server.URL, "LLM|muse-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	museClient, ok := client.(*museClient)
	if !ok {
		t.Fatalf("client = %T, want *museClient", client)
	}
	museClient.SetForceKeyResolver(func(string) string { return "LLM|muse-key" })

	err = client.Stream(context.Background(), ChatRequest{
		Model:    "muse-spark-1.5",
		System:   "be brief",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
		Tools:    []ToolDef{{Name: "read_file", Description: "read", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}},
	}, func(StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if path != "/responses" {
		t.Errorf("path = %q, want /responses", path)
	}
	wantUA := museUserAgent
	if ua != wantUA {
		t.Errorf("User-Agent = %q, want %q", ua, wantUA)
	}
	if !strings.HasPrefix(ua, "muse-build/1.4.24") {
		t.Errorf("User-Agent %q is not a muse-build 1.4.24 identity", ua)
	}
	if clientID != "tbh:exec" {
		t.Errorf("x-client-id = %q, want tbh:exec", clientID)
	}
	if agentRole != "main" {
		t.Errorf("x-tbh-agent-role = %q, want main", agentRole)
	}
	if sessionID == "" || gatewaySession != sessionID || rootSession != sessionID || threadID == "" {
		t.Errorf("session headers inconsistent: session=%q gateway=%q root=%q thread=%q", sessionID, gatewaySession, rootSession, threadID)
	}
	if len(tools) != 1 {
		t.Fatalf("tools field = %v, want one namespace tool", tools)
	}
	if tools[0]["type"] != "namespace" || tools[0]["name"] != "muse" {
		t.Errorf("tools[0] = %v, want muse namespace", tools[0])
	}
	if inner, _ := tools[0]["tools"].([]any); len(inner) != 1 {
		t.Errorf("namespace tools = %v, want 1 inner tool", tools[0]["tools"])
	}
}
