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
	"time"
)

// fastMuseRetry removes the retry backoff so the retry tests do not wait.
func fastMuseRetry(t *testing.T) {
	t.Helper()
	previous := museRetryWait
	museRetryWait = func(int) time.Duration { return 0 }
	t.Cleanup(func() { museRetryWait = previous })
}

// TestMuseStreamUsesTheResponsesAPI proves the Muse client posts to /responses
// with the Meta identity headers and decodes the typed event feed, the same
// shape the Codex client uses.
func TestMuseStreamUsesTheResponsesAPI(t *testing.T) {
	var gotPath, gotAuth, gotAgent, gotClientID, gotAPIVersion, gotSessionID string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotAuth = request.Header.Get("Authorization")
		gotAgent = request.Header.Get("User-Agent")
		gotClientID = request.Header.Get("X-Client-Id")
		gotAPIVersion = request.Header.Get("x-api-version")
		gotSessionID = request.Header.Get("session_id")
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
	if gotAPIVersion != "1.0.0" {
		t.Errorf("x-api-version = %q, want 1.0.0", gotAPIVersion)
	}
	if gotSessionID == "" {
		t.Errorf("session_id should not be empty")
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

// TestMuseStampsFunctionCallItems pins the item shape Meta requires: a replayed
// function_call without id and status makes the backend answer 404
// model_not_found, so both fields must be present on the wire.
func TestMuseStampsFunctionCallItems(t *testing.T) {
	payload := (&museClient{}).payload(ChatRequest{
		Model: "muse-spark-1.3",
		Messages: []Message{
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_9", Name: "read_file", Arguments: "{}"}}},
			{Role: RoleTool, ToolID: "call_9", Content: "ok"},
		},
	})
	input, ok := payload["input"].([]map[string]any)
	if !ok {
		t.Fatalf("input = %T, want a list of items", payload["input"])
	}
	var call, output map[string]any
	for _, item := range input {
		switch item["type"] {
		case "function_call":
			call = item
		case "function_call_output":
			output = item
		}
	}
	if call == nil {
		t.Fatalf("no function_call item in %v", input)
	}
	if call["id"] != "fc_call_9" {
		t.Errorf("function_call id = %v, want fc_call_9", call["id"])
	}
	if call["status"] != "completed" {
		t.Errorf("function_call status = %v, want completed", call["status"])
	}
	if output == nil || output["call_id"] != "call_9" {
		t.Errorf("function_call_output = %v", output)
	}
}

// TestMuseRetriesTransientNotFoundOnTheSameKey pins that a 404 which clears on
// its own is replayed with the same key, without minting a new one. Meta is
// intermittently overloaded and answers a healthy request with 404.
func TestMuseRetriesTransientNotFoundOnTheSameKey(t *testing.T) {
	fastMuseRetry(t)
	var hits int
	var auths []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		auths = append(auths, request.Header.Get("Authorization"))
		if hits == 1 {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"error":{"code":"model_not_found","message":"The requested model was not found."}}`))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "muse", Label: "Meta Muse Code", Kind: KindMuse}, server.URL, "LLM|muse-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	client.(interface{ SetForceKeyResolver(KeyResolver) }).SetForceKeyResolver(func(string) string { return "LLM|fresh" })

	var text strings.Builder
	err = client.Stream(context.Background(), ChatRequest{
		Model:    "muse-spark-1.3",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(event StreamEvent) error {
		if event.Type == EventTextDelta {
			text.WriteString(event.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2 (one transient 404 then success)", hits)
	}
	if auths[0] != "Bearer LLM|muse-key" || auths[1] != "Bearer LLM|muse-key" {
		t.Errorf("a transient 404 must be retried on the same key, got %v", auths)
	}
	if text.String() != "ok" {
		t.Errorf("text = %q, want ok", text.String())
	}
}

// TestMuseRemintsWhenNotFoundPersists pins the renewal path: when every retry
// of the stored key answers 404, the client mints a fresh key and replays.
func TestMuseRemintsWhenNotFoundPersists(t *testing.T) {
	fastMuseRetry(t)
	var hits int
	var auths []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		auth := request.Header.Get("Authorization")
		auths = append(auths, auth)
		if auth == "Bearer LLM|stale" {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"error":{"code":"model_not_found","message":"The requested model was not found."}}`))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "muse", Label: "Meta Muse Code", Kind: KindMuse}, server.URL, "LLM|stale")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	client.(interface{ SetForceKeyResolver(KeyResolver) }).SetForceKeyResolver(func(string) string { return "LLM|fresh" })

	var text strings.Builder
	err = client.Stream(context.Background(), ChatRequest{
		Model:    "muse-spark-1.3",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(event StreamEvent) error {
		if event.Type == EventTextDelta {
			text.WriteString(event.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if hits != museRetryAttempts+1 {
		t.Fatalf("hits = %d, want %d (stale retried, then one fresh)", hits, museRetryAttempts+1)
	}
	if auths[len(auths)-1] != "Bearer LLM|fresh" {
		t.Errorf("the last attempt should use the fresh key, got %v", auths)
	}
	if text.String() != "ok" {
		t.Errorf("text = %q, want ok", text.String())
	}
}

// TestMuseResetsSessionAndRetriesOnSessionError proves that like Antigravity,
// a session or unauthenticated error resets the session and retries seamlessly.
func TestMuseResetsSessionAndRetriesOnSessionError(t *testing.T) {
	var hits int
	var sessions []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		sessions = append(sessions, request.Header.Get("session_id"))
		if hits == 1 {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"error":{"message":"session expired or invalid"}}`))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"resumed\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "muse", Label: "Meta Muse Code", Kind: KindMuse}, server.URL, "LLM|session-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}

	var text strings.Builder
	err = client.Stream(context.Background(), ChatRequest{
		Model:    "muse-spark-1.3",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(event StreamEvent) error {
		if event.Type == EventTextDelta {
			text.WriteString(event.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2 (retry on session error)", hits)
	}
	if len(sessions) != 2 || sessions[0] == sessions[1] {
		t.Errorf("session_id should have changed across retry, got: %v", sessions)
	}
	if text.String() != "resumed" {
		t.Errorf("text = %q, want resumed", text.String())
	}
}

// TestMuseDoesNotRetryOnAnOrdinaryError keeps the renewal scoped to a stale
// credential: a real 400 must surface once, not be retried as if a mint helps.
func TestMuseDoesNotRetryOnAnOrdinaryError(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "muse", Label: "Meta Muse Code", Kind: KindMuse}, server.URL, "LLM|key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	client.(interface{ SetForceKeyResolver(KeyResolver) }).SetForceKeyResolver(func(string) string { return "LLM|other" })

	err = client.Stream(context.Background(), ChatRequest{
		Model:    "muse-spark-1.3",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("a 400 must be reported")
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1 (no retry for a non-stale error)", hits)
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
