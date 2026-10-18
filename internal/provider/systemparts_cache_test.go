package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOpenAISystemPartsSplitForPrefixCache pins the wire shape the prompt cache
// depends on: the structured system parts must arrive as separate system
// messages, static prefix first, dynamic plan after. A client that folded them
// into one system message put the changing todo plan in the middle of the
// cached prefix, invalidating it on every plan update.
func TestOpenAISystemPartsSplitForPrefixCache(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		decoder := json.NewDecoder(request.Body)
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		bodies = append(bodies, payload)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "openai", Label: "OpenAI", Kind: KindOpenAI}, server.URL, "test-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	events := make(chan struct{}, 1)
	go func() {
		_ = client.Stream(t.Context(), ChatRequest{
			Model: "gpt-test",
			SystemParts: []string{
				"static prefix with the environment block",
				"working plan: one item in progress",
			},
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
		}, func(StreamEvent) error { return nil })
		events <- struct{}{}
	}()
	<-events

	if len(bodies) != 1 {
		t.Fatalf("requests = %d, want 1", len(bodies))
	}
	messages, ok := bodies[0]["messages"].([]any)
	if !ok {
		t.Fatalf("payload messages has type %T", bodies[0]["messages"])
	}
	var systems []string
	for _, entry := range messages {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := item["role"].(string); role != "system" {
			continue
		}
		content, _ := item["content"].(string)
		systems = append(systems, content)
	}
	if len(systems) != 2 {
		t.Fatalf("system messages = %d (%q), want 2", len(systems), systems)
	}
	if systems[0] != "static prefix with the environment block" {
		t.Errorf("first system = %q, want the static prefix", systems[0])
	}
	if systems[1] != "working plan: one item in progress" {
		t.Errorf("second system = %q, want the dynamic plan", systems[1])
	}
}

// TestGoogleSystemPartsSplitForPrefixCache pins the same contract on the
// Gemini wire: systemInstruction parts, static prefix first.
func TestGoogleSystemPartsSplitForPrefixCache(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		decoder := json.NewDecoder(request.Body)
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		bodies = append(bodies, payload)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {}\n\n"))
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "google", Label: "Google", Kind: KindGoogle}, server.URL, "test-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	_ = client.Stream(t.Context(), ChatRequest{
		Model: "gemini-test",
		SystemParts: []string{
			"static prefix",
			"working plan",
		},
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })

	if len(bodies) != 1 {
		t.Fatalf("requests = %d, want 1", len(bodies))
	}
	instruction, ok := bodies[0]["systemInstruction"].(map[string]any)
	if !ok {
		t.Fatalf("systemInstruction has type %T", bodies[0]["systemInstruction"])
	}
	parts, ok := instruction["parts"].([]any)
	if !ok {
		t.Fatalf("parts has type %T", instruction["parts"])
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	first, _ := parts[0].(map[string]any)
	if text, _ := first["text"].(string); text != "static prefix" {
		t.Errorf("first part = %q, want the static prefix", text)
	}
	second, _ := parts[1].(map[string]any)
	if text, _ := second["text"].(string); text != "working plan" {
		t.Errorf("second part = %q, want the dynamic plan", text)
	}
}

// TestJoinSystemPartsPreservesOrder pins the single-string formats: the
// Responses instructions field and the plain Anthropic system string.
func TestJoinSystemPartsPreservesOrder(t *testing.T) {
	joined := joinSystemParts([]string{"prefix", "", "plan"})
	if joined != "prefix\n\nplan" {
		t.Errorf("joinSystemParts = %q, want prefix then plan with a blank line", joined)
	}
	if joinSystemParts(nil) != "" {
		t.Errorf("joinSystemParts(nil) = %q, want empty", joinSystemParts(nil))
	}
}
