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

// thinkingStream is a Messages stream with one thinking block (text then
// signature), one redacted block and one tool call, as the API writes them.
const thinkingStream = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"look at "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"the file"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-abc"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque-data"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.go\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}

`

func thinkingClient(t *testing.T, url string) Client {
	t.Helper()
	client, err := newHTTPClient(Provider{ID: "anthropic", Label: "Anthropic", Kind: KindAnthropic}, url, "key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	return client
}

func TestAdaptiveThinkingPayloadAndContextManagement(t *testing.T) {
	var body map[string]any
	var beta string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		beta = request.Header.Get("anthropic-beta")
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, thinkingStream)
	}))
	defer server.Close()

	temperature := 0.2
	err := thinkingClient(t, server.URL).Stream(context.Background(), ChatRequest{
		Model: "claude-opus-5-5", Temperature: &temperature, Effort: EffortXHigh,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	thinking, _ := body["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" || thinking["display"] != "summarized" {
		t.Errorf("thinking = %v, want adaptive summarized", thinking)
	}
	management, _ := body["context_management"].(map[string]any)
	edits, _ := management["edits"].([]any)
	edit, _ := edits[0].(map[string]any)
	if len(edits) != 1 || edit["type"] != "clear_thinking_20251015" || edit["keep"] != "all" {
		t.Errorf("context_management = %v", management)
	}
	if _, has := body["temperature"]; has {
		t.Errorf("a thinking request must drop temperature, got %v", body["temperature"])
	}
	if body["max_tokens"] != float64(128000) {
		t.Errorf("max_tokens = %v, want the CLI ceiling 128000 for a thinking request", body["max_tokens"])
	}
	if !strings.Contains(beta, "context-management-2025-06-27") {
		t.Errorf("anthropic-beta = %q, want the context-management beta", beta)
	}
	output, _ := body["output_config"].(map[string]any)
	if output["effort"] != "xhigh" {
		t.Errorf("output_config = %v", output)
	}
}

func TestThinkingIsLeftOutForModelsThatLackIt(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	for _, model := range []string{"claude-haiku-4-5", "claude-sonnet-4-6"} {
		body = nil
		if err := thinkingClient(t, server.URL).Stream(context.Background(), ChatRequest{Model: model, Messages: []Message{{Role: RoleUser, Content: "hi"}}}, func(StreamEvent) error { return nil }); err != nil {
			t.Fatalf("Stream %s: %v", model, err)
		}
		if _, has := body["thinking"]; has {
			t.Errorf("%s was sent a thinking parameter", model)
		}
		if _, has := body["context_management"]; has {
			t.Errorf("%s was sent context_management", model)
		}
	}
	t.Setenv("TERMIXGO_CLAUDE_THINKING", "off")
	body = nil
	if err := thinkingClient(t, server.URL).Stream(context.Background(), ChatRequest{Model: "claude-opus-5-5", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, func(StreamEvent) error { return nil }); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, has := body["thinking"]; has {
		t.Errorf("TERMIXGO_CLAUDE_THINKING=off still sent thinking")
	}
}

// TestThinkingBlocksAreKeptAndReplayed walks the loop that adaptive thinking
// requires: the signed blocks of a tool-calling step come back on the next
// request, in front of the call, and only for the model that wrote them.
func TestThinkingBlocksAreKeptAndReplayed(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		requests = append(requests, body)
		writer.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			fmt.Fprint(writer, thinkingStream)
			return
		}
		fmt.Fprint(writer, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	client := thinkingClient(t, server.URL)

	var items []ReasoningItem
	var calls []ToolCall
	var reasoning strings.Builder
	err := client.Stream(context.Background(), ChatRequest{Model: "claude-opus-5-5", Messages: []Message{{Role: RoleUser, Content: "read it"}}}, func(event StreamEvent) error {
		switch event.Type {
		case EventReasoningItem:
			items = append(items, *event.ReasoningItem)
		case EventToolCall:
			calls = append(calls, *event.ToolCall)
		case EventReasoningDelta:
			reasoning.WriteString(event.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("first step: %v", err)
	}
	if len(items) != 2 || items[0].Kind != reasoningKindThinking || items[0].EncryptedContent != "sig-abc" || items[0].Summary[0] != "look at the file" || items[1].Kind != reasoningKindRedacted || items[1].EncryptedContent != "opaque-data" {
		t.Fatalf("items = %+v", items)
	}
	if reasoning.String() != "look at the file" || len(calls) != 1 || calls[0].Name != "read_file" {
		t.Fatalf("reasoning=%q calls=%+v", reasoning.String(), calls)
	}

	history := []Message{
		{Role: RoleUser, Content: "read it"},
		{Role: RoleAssistant, ToolCalls: calls, ReasoningItems: items},
		{Role: RoleTool, ToolID: calls[0].ID, Name: "read_file", Content: "package a"},
	}
	if err := client.Stream(context.Background(), ChatRequest{Model: "claude-opus-5-5", Messages: history}, func(StreamEvent) error { return nil }); err != nil {
		t.Fatalf("second step: %v", err)
	}
	messages, _ := requests[1]["messages"].([]any)
	assistant, _ := messages[1].(map[string]any)
	blocks, _ := assistant["content"].([]any)
	kinds := []string{}
	for _, block := range blocks {
		kinds = append(kinds, block.(map[string]any)["type"].(string))
	}
	if strings.Join(kinds, ",") != "thinking,redacted_thinking,tool_use" {
		t.Fatalf("assistant blocks = %v, want the thinking blocks ahead of the call", kinds)
	}
	first, _ := blocks[0].(map[string]any)
	if first["signature"] != "sig-abc" || first["thinking"] != "look at the file" {
		t.Errorf("thinking block = %v", first)
	}

	// Another model's signature is not valid here and is not sent.
	requests = nil
	if err := client.Stream(context.Background(), ChatRequest{Model: "claude-fable-5-2", Messages: history}, func(StreamEvent) error { return nil }); err != nil {
		t.Fatalf("other model: %v", err)
	}
	messages, _ = requests[0]["messages"].([]any)
	assistant, _ = messages[1].(map[string]any)
	blocks, _ = assistant["content"].([]any)
	if len(blocks) != 1 || blocks[0].(map[string]any)["type"] != "tool_use" {
		t.Errorf("blocks for another model = %v, want only the tool call", blocks)
	}
}

func TestThinkingRejectionRetriesAsAPlainRequest(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		requests = append(requests, body)
		if len(requests) == 1 {
			writer.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(writer, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid signature in thinking block"}}`)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	history := []Message{
		{Role: RoleUser, Content: "go"},
		{Role: RoleAssistant, Content: "ok", ReasoningItems: []ReasoningItem{{Kind: reasoningKindThinking, ID: "claude-opus-5-5", EncryptedContent: "stale", Summary: []string{"x"}}}},
		{Role: RoleUser, Content: "again"},
	}
	if err := thinkingClient(t, server.URL).Stream(context.Background(), ChatRequest{Model: "claude-opus-5-5", Messages: history}, func(StreamEvent) error { return nil }); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want the rejected one and one retry", len(requests))
	}
	if _, has := requests[1]["thinking"]; has {
		t.Errorf("the retry still carried thinking")
	}
	if strings.Contains(fmt.Sprint(requests[1]["messages"]), "stale") {
		t.Errorf("the retry still carried the rejected thinking block")
	}
}

func TestResponsesEncoderSkipsAnthropicThinking(t *testing.T) {
	input := encodeResponsesInput(ChatRequest{Messages: []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "hello", ReasoningItems: []ReasoningItem{{Kind: reasoningKindThinking, ID: "claude-opus-5-5", EncryptedContent: "sig"}}},
	}})
	for _, item := range input {
		if item["type"] == "reasoning" {
			t.Fatalf("an Anthropic thinking block was replayed as a Responses reasoning item: %v", item)
		}
	}
}
