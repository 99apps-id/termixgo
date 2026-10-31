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

// TestCodexStreamUsesTheResponsesAPI proves the Codex client posts to
// /responses with the account header and decodes the typed event feed.
func TestCodexStreamUsesTheResponsesAPI(t *testing.T) {
	var gotPath, gotAuth, gotAccount, gotOriginator, gotVersion, gotAgent, gotSessionID string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotAuth = request.Header.Get("Authorization")
		gotAccount = request.Header.Get("ChatGPT-Account-ID")
		gotOriginator = request.Header.Get("originator")
		gotVersion = request.Header.Get("version")
		gotAgent = request.Header.Get("User-Agent")
		gotSessionID = request.Header.Get("session_id")
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &gotBody)

		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello \"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"thinking\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"a\\\"}\"}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n")
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "openai-codex", Label: "Codex", Kind: KindOpenAI}, server.URL, "oauth-token")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	codex, ok := client.(*codexClient)
	if !ok {
		t.Fatalf("client = %T, want *codexClient", client)
	}
	codex.SetAccountID("acct_9")

	var text, reasoning strings.Builder
	var calls []ToolCall
	var usage Usage
	err = codex.Stream(context.Background(), ChatRequest{
		Model:  "gpt-5.3-codex",
		System: "be brief",
		Messages: []Message{
			{Role: RoleUser, Content: "read a"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_0", Name: "grep", Arguments: "{}"}}},
			{Role: RoleTool, ToolID: "call_0", Name: "grep", Content: "done"},
		},
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
	if gotAuth != "Bearer oauth-token" || gotAccount != "acct_9" || gotOriginator != "codex_cli_rs" {
		t.Errorf("headers: auth=%q account=%q originator=%q", gotAuth, gotAccount, gotOriginator)
	}
	// The backend gates newer models on the Codex CLI identity, so the version
	// header and User-Agent must carry a real release, not a placeholder.
	if gotVersion != codexCLIVersion || gotAgent != "codex_cli_rs/"+codexCLIVersion {
		t.Errorf("identity headers: version=%q user-agent=%q", gotVersion, gotAgent)
	}
	if gotSessionID == "" {
		t.Errorf("session_id header missing")
	}
	if gotBody["instructions"] != "be brief" || gotBody["store"] != false {
		t.Errorf("body = %v", gotBody)
	}
	// The reasoning controls travel as one object asking for an auto
	// summary: without the summary a reasoning step streams nothing the
	// agent loop can see. The top-level reasoning_effort spelling is the
	// chat-completions one and must not ride along.
	reasoningBody, ok := gotBody["reasoning"].(map[string]any)
	if !ok || reasoningBody["summary"] != "auto" {
		t.Errorf("reasoning = %v, want an object asking for an auto summary", gotBody["reasoning"])
	}
	if _, present := gotBody["reasoning_effort"]; present {
		t.Errorf("a codex request must not carry a top-level reasoning_effort: %v", gotBody)
	}
	// The encrypted reasoning include is what lets the next step hand this
	// step's reasoning items back to a store: false backend.
	include, ok := gotBody["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v, want the encrypted reasoning content", gotBody["include"])
	}
	if text.String() != "Hello " || reasoning.String() != "thinking" {
		t.Errorf("text=%q reasoning=%q", text.String(), reasoning.String())
	}
	if len(calls) != 1 || calls[0].Name != "read_file" || calls[0].ID != "call_1" {
		t.Errorf("calls = %+v", calls)
	}
	if usage.TotalTokens != 15 || usage.PromptTokens != 10 {
		t.Errorf("usage = %+v", usage)
	}
}

// TestCodexInputUsesResponseItems pins the request shape: tool results become
// function_call_output items, not chat tool messages.
func TestCodexInputUsesResponseItems(t *testing.T) {
	items := encodeResponsesInput(ChatRequest{Messages: []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "t", Arguments: "{}"}}},
		{Role: RoleTool, ToolID: "c1", Content: "out"},
	}})
	if len(items) != 3 {
		t.Fatalf("items = %v", items)
	}
	if items[0]["type"] != "message" || items[1]["type"] != "function_call" || items[2]["type"] != "function_call_output" {
		t.Errorf("item types = %v %v %v", items[0]["type"], items[1]["type"], items[2]["type"])
	}
	if items[2]["call_id"] != "c1" {
		t.Errorf("tool result should carry call_id, got %v", items[2])
	}
}

// TestCodexResponsesLiteMovesToolsAndInstructions covers the lite models: they
// take tools and instructions as input prefix items and reject the top-level
// tools and instructions fields the standard models use.
func TestCodexResponsesLiteMovesToolsAndInstructions(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &gotBody)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "openai-codex", Label: "Codex", Kind: KindOpenAI}, server.URL, "token")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	err = client.Stream(context.Background(), ChatRequest{
		Model:    "gpt-6-sol",
		System:   "be brief",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools: []ToolDef{{
			Name:   "read_file",
			Schema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		}},
	}, func(StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if _, present := gotBody["tools"]; present {
		t.Errorf("a lite request must not carry top-level tools")
	}
	if _, present := gotBody["instructions"]; present {
		t.Errorf("a lite request must not carry top-level instructions")
	}
	input, ok := gotBody["input"].([]any)
	if !ok || len(input) < 2 {
		t.Fatalf("input = %#v", gotBody["input"])
	}
	prefix := input[0].(map[string]any)
	if prefix["type"] != "additional_tools" || prefix["role"] != "developer" {
		t.Errorf("input[0] = %#v, want the additional_tools prefix", prefix)
	}
	instruction := input[1].(map[string]any)
	if instruction["type"] != "message" || instruction["role"] != "developer" {
		t.Errorf("input[1] = %#v, want the developer instructions", instruction)
	}
	reasoning, _ := gotBody["reasoning"].(map[string]any)
	if reasoning["context"] != "all_turns" {
		t.Errorf("reasoning = %#v, want context all_turns", reasoning)
	}
	if reasoning["summary"] != "auto" {
		t.Errorf("reasoning = %#v, want an auto summary so the thinking streams back", reasoning)
	}
	// The lite models reject the top-level field, so writing both it and the
	// reasoning object would make every request fail.
	if _, present := gotBody["reasoning_effort"]; present {
		t.Errorf("a lite request must not also carry a top-level reasoning_effort")
	}
}

// TestCodexLiteCarriesTheEffortInsideReasoning pins the level's route on a lite
// model: it travels in the reasoning object, which is the only place the model
// accepts it.
func TestCodexLiteCarriesTheEffortInsideReasoning(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &gotBody)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "openai-codex", Label: "Codex", Kind: KindOpenAI}, server.URL, "token")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	err = client.Stream(context.Background(), ChatRequest{
		Model:    "gpt-6-sol",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Effort:   EffortLow,
	}, func(StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	reasoning, _ := gotBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "low" {
		t.Errorf("reasoning.effort = %v, want low", reasoning["effort"])
	}
}

// TestDecodeResponsesStreamCapturesReasoningItem proves the decoder keeps a
// completed reasoning item whole: with store: false the item is the only
// copy of the step's thought, and the next request hands it back.
func TestDecodeResponsesStreamCapturesReasoningItem(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","encrypted_content":"enc_abc","summary":[{"type":"summary_text","text":"thought one"}]}}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		``,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
		``,
	}, "\n")
	var items []ReasoningItem
	err := decodeResponsesStream("Codex", strings.NewReader(sse), func(event StreamEvent) error {
		if event.Type == EventReasoningItem {
			items = append(items, *event.ReasoningItem)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("decodeResponsesStream: %v", err)
	}
	if len(items) != 1 || items[0].ID != "rs_1" || items[0].EncryptedContent != "enc_abc" ||
		len(items[0].Summary) != 1 || items[0].Summary[0] != "thought one" {
		t.Errorf("items = %+v, want the one reasoning item kept whole", items)
	}
}

// TestCodexInputReplaysReasoningItems proves an assistant turn's stored
// reasoning items go back into the next request ahead of its words and
// calls, in the shape the backend issued them.
func TestCodexInputReplaysReasoningItems(t *testing.T) {
	input := encodeResponsesInput(ChatRequest{Messages: []Message{
		{Role: RoleUser, Content: "read a"},
		{Role: RoleAssistant, Content: "working", ReasoningItems: []ReasoningItem{
			{ID: "rs_1", EncryptedContent: "enc_abc", Summary: []string{"thought one"}},
		}, ToolCalls: []ToolCall{{ID: "call_1", Name: "read_file", Arguments: "{}"}}},
	}})
	if len(input) != 4 {
		t.Fatalf("input = %#v, want user, reasoning, message, function_call", input)
	}
	item := input[1]
	if item["type"] != "reasoning" || item["id"] != "rs_1" || item["encrypted_content"] != "enc_abc" {
		t.Errorf("input[1] = %#v, want the replayed reasoning item", item)
	}
	summary, ok := item["summary"].([]map[string]any)
	if !ok || len(summary) != 1 || summary[0]["type"] != "summary_text" || summary[0]["text"] != "thought one" {
		t.Errorf("summary = %#v, want the one summary_text part", item["summary"])
	}
	if input[2]["type"] != "message" || input[3]["type"] != "function_call" {
		t.Errorf("input order = %#v, want the reasoning item before the words and the call", input)
	}
}

// TestCodexInputAlwaysSendsReasoningSummary proves a stored reasoning item
// that arrived without summary parts still replays with the summary key
// present, as an empty array: the backend requires the key on a replayed
// reasoning item and rejects the whole request when it is omitted
// ("Missing required parameter: 'input[N].summary'").
func TestCodexInputAlwaysSendsReasoningSummary(t *testing.T) {
	input := encodeResponsesInput(ChatRequest{Messages: []Message{
		{Role: RoleAssistant, Content: "working", ReasoningItems: []ReasoningItem{
			{ID: "rs_2", EncryptedContent: "enc_def"},
		}},
	}})
	if len(input) != 2 {
		t.Fatalf("input = %#v, want reasoning, message", input)
	}
	raw, present := input[0]["summary"]
	if !present {
		t.Fatalf("input[0] = %#v, want the summary key present on a summary-less item", input[0])
	}
	summary, ok := raw.([]map[string]any)
	if !ok || len(summary) != 0 {
		t.Errorf("summary = %#v, want an empty summary array", raw)
	}
}

// TestDecodeResponsesStreamUsesFunctionCallArgumentsDone proves the decoder
// recovers a tool call's arguments from the dedicated finalize event. Meta
// streams the arguments in response.function_call_arguments.delta/.done and can
// leave the output_item.done copy empty; storing that blank string replays as
// invalid JSON and Meta rejects every later turn with HTTP 400.
func TestDecodeResponsesStreamUsesFunctionCallArgumentsDone(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"path\":"}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"\"a.go\"}"}`,
		``,
		`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"path\":\"a.go\"}"}`,
		``,
		`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":""}}`,
		``,
	}, "\n")

	var calls []ToolCall
	if err := decodeResponsesStream("test", strings.NewReader(sse), func(event StreamEvent) error {
		if event.Type == EventToolCall {
			calls = append(calls, *event.ToolCall)
		}
		return nil
	}); err != nil {
		t.Fatalf("decodeResponsesStream: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want one", calls)
	}
	if calls[0].Arguments != `{"path":"a.go"}` {
		t.Errorf("arguments = %q, want the finalized JSON from the .done event", calls[0].Arguments)
	}
	if calls[0].ID != "call_1" || calls[0].Name != "read_file" {
		t.Errorf("call = %+v, want call_1/read_file", calls[0])
	}
}

// TestCodexResponsesLiteModelsCoversNewerGenerations covers the additional
// models that share the responses-lite backend contract: gpt-6-astra and the
// GPT-5.6 family. They must be rewritten the same way as gpt-6-sol.
func TestCodexResponsesLiteModelsCoversNewerGenerations(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.6-terra"} {
		payload := map[string]any{
			"model":        model,
			"messages":     []map[string]any{{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "hi"}}}},
			"tools":        []map[string]any{{"type": "function", "name": "t", "strict": true}},
			"instructions": "be brief",
		}
		applyResponsesLite(payload, []map[string]any{{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "hi"}}}}, payload["tools"].([]map[string]any), "be brief", "medium")
		if _, present := payload["tools"]; present {
			t.Errorf("%s: lite request must not carry top-level tools", model)
		}
		if _, present := payload["instructions"]; present {
			t.Errorf("%s: lite request must not carry top-level instructions", model)
		}
		input := payload["input"].([]map[string]any)
		if len(input) < 2 {
			t.Errorf("%s: input prefix must contain at least tools and instructions", model)
			continue
		}
		if input[0]["type"] != "additional_tools" {
			t.Errorf("%s: first input item must be additional_tools", model)
		}
	}
}

// TestDecodeResponsesStreamFallsBackToArgumentDeltas covers a finalize event
// that carries no arguments: the decoder assembles the streamed deltas instead
// of emitting an empty string.
func TestDecodeResponsesStreamFallsBackToArgumentDeltas(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_9","delta":"{\"q\":"}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_9","delta":"1}"}`,
		``,
		`data: {"type":"response.function_call_arguments.done","item_id":"fc_9","arguments":""}`,
		``,
		`data: {"type":"response.output_item.done","item":{"id":"fc_9","type":"function_call","call_id":"call_9","name":"grep","arguments":""}}`,
		``,
	}, "\n")

	var calls []ToolCall
	_ = decodeResponsesStream("test", strings.NewReader(sse), func(event StreamEvent) error {
		if event.Type == EventToolCall {
			calls = append(calls, *event.ToolCall)
		}
		return nil
	})
	if len(calls) != 1 || calls[0].Arguments != `{"q":1}` {
		t.Fatalf("calls = %+v, want the assembled deltas", calls)
	}
}

// TestCodexInputCarriesNoMetaPhase pins that the shared encoder leaves the
// Meta-specific phase field out, so the ChatGPT backend Codex targets is never
// sent a field it does not accept. The Muse client adds it in its own pass.
func TestCodexInputCarriesNoMetaPhase(t *testing.T) {
	items := encodeResponsesInput(ChatRequest{Messages: []Message{
		{Role: RoleAssistant, Content: "thinking", ToolCalls: []ToolCall{{ID: "c1", Name: "t", Arguments: "{}"}}},
	}})
	for _, item := range items {
		if _, present := item["phase"]; present {
			t.Errorf("the shared encoder must not set the Meta-specific phase field: %v", item)
		}
	}
}

// A response cut at the backend's output ceiling is a truncated step, not
// a failed one: the decoder must still harvest its usage, then report the
// cut as an EventIncomplete so the loop can continue from the partial
// output instead of the run dying on a limit the client never set.
func TestDecodeResponsesStreamReportsOutputCeilingAsEvent(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
		``,
		`data: {"type":"response.incomplete","response":{"usage":{"input_tokens":10,"output_tokens":99,"total_tokens":109},"incomplete_details":{"reason":"max_output_tokens"}}}`,
		``,
	}, "\n")

	var usages int
	var incomplete []string
	err := decodeResponsesStream("test", strings.NewReader(sse), func(event StreamEvent) error {
		switch event.Type {
		case EventUsage:
			usages++
		case EventIncomplete:
			incomplete = append(incomplete, event.IncompleteReason)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("a step cut at the output ceiling must not fail the stream: %v", err)
	}
	if len(incomplete) != 1 || incomplete[0] != "max_output_tokens" {
		t.Errorf("incomplete events = %v, want one max_output_tokens event", incomplete)
	}
	if usages != 1 {
		t.Errorf("usage events = %d, want the incomplete response's usage harvested once", usages)
	}
}

// An incomplete response for any other reason (a content filter) is not a
// step the loop should quietly retry: the decoder must still harvest its
// usage, then end the stream in an error naming the reason, so the agent
// loop reports a provider failure instead of counting the step as idle.
func TestDecodeResponsesStreamErrorsOnOtherIncompleteReasons(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.incomplete","response":{"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"incomplete_details":{"reason":"content_filter"}}}`,
		``,
	}, "\n")

	var usages int
	err := decodeResponsesStream("test", strings.NewReader(sse), func(event StreamEvent) error {
		if event.Type == EventUsage {
			usages++
		}
		return nil
	})
	if err == nil {
		t.Fatalf("an incomplete response cut by a content filter must end the stream in an error")
	}
	if !strings.Contains(err.Error(), "incomplete") || !strings.Contains(err.Error(), "content_filter") {
		t.Errorf("error = %q, want it to name the incomplete reason", err)
	}
	if usages != 1 {
		t.Errorf("usage events = %d, want the incomplete response's usage harvested once", usages)
	}
}
