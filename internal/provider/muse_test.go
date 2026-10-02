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

// TestMuseStreamUsesTheResponsesAPI proves the Muse client posts to /responses
// with the Meta identity headers and decodes the typed event feed, the same
// shape the Codex client uses.
func TestMuseStreamUsesTheResponsesAPI(t *testing.T) {
	var gotPath, gotAuth, gotAgent, gotClientID string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotAuth = request.Header.Get("Authorization")
		gotAgent = request.Header.Get("User-Agent")
		gotClientID = request.Header.Get("X-Client-Id")
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &gotBody)

		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi \"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"think\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"{}\"}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":3,\"total_tokens\":10}}}\n\n")
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "muse", Label: "Meta Muse Code", Kind: KindMuse}, server.URL, "LLM|muse-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	if _, ok := client.(*museClient); !ok {
		t.Fatalf("client = %T, want *museClient", client)
	}

	var text, reasoning strings.Builder
	var calls []ToolCall
	var usage Usage
	err = client.Stream(context.Background(), ChatRequest{
		Model:  "muse-spark-1.3",
		System: "be brief",
		Messages: []Message{
			{Role: RoleUser, Content: "hello"},
			{Role: RoleTool, ToolID: "call_0", Name: "grep", Content: "out"},
		},
		Tools: []ToolDef{{Name: "read_file", Description: "read", Schema: map[string]any{"type": "object"}}},
	}, func(event StreamEvent) error {
		switch event.Type {
		case EventTextDelta:
			text.WriteString(event.Text)
		case EventReasoningDelta:
			reasoning.WriteString(event.Text)
		case EventToolCall:
			calls = append(calls, *event.ToolCall)
		case EventUsage:
			usage = *event.Usage
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if gotPath != "/responses" {
		t.Errorf("path = %q, want /responses", gotPath)
	}
	if gotAuth != "Bearer LLM|muse-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAgent != museUserAgent {
		t.Errorf("User-Agent = %q, want %q", gotAgent, museUserAgent)
	}
	if gotClientID != "tbh:tui" {
		t.Errorf("X-Client-Id = %q, want tbh:tui", gotClientID)
	}
	if gotBody["model"] != "muse-spark-1.3" || gotBody["instructions"] != "be brief" {
		t.Errorf("body = %v", gotBody)
	}
	if _, present := gotBody["input"]; !present {
		t.Errorf("a Responses request must carry input items: %v", gotBody)
	}
	if text.String() != "Hi " || reasoning.String() != "think" {
		t.Errorf("text=%q reasoning=%q", text.String(), reasoning.String())
	}
	if len(calls) != 1 || calls[0].Name != "read_file" || calls[0].ID != "call_1" {
		t.Errorf("calls = %+v", calls)
	}
	if usage.TotalTokens != 10 || usage.PromptTokens != 7 {
		t.Errorf("usage = %+v", usage)
	}
}

// TestMuseProviderIsRegistered keeps the catalogue, the kind and the default
// endpoint in step: a Muse login is useless if the provider is not reachable by
// id or the models are not offered.
func TestMuseProviderIsRegistered(t *testing.T) {
	info, ok := ByID("muse")
	if !ok {
		t.Fatal("muse provider is not registered")
	}
	if info.Kind != KindMuse || !info.OAuth || info.DefaultBaseURL != "https://api.meta.ai/v1" {
		t.Errorf("muse provider = %+v", info)
	}
	models := ModelsFor("muse")
	if len(models) == 0 {
		t.Fatal("muse has no models in the catalogue")
	}
	found := false
	for _, model := range models {
		if model.ID == "muse-spark-1.3" {
			found = true
		}
	}
	if !found {
		t.Errorf("muse-spark-1.3 is missing from %v", models)
	}
}
