package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestAntigravityStreamOnboardsAndDecodes proves the client discovers the
// project, builds the Cloud Code envelope, and decodes the wrapped stream.
func TestAntigravityStreamOnboardsAndDecodes(t *testing.T) {
	var gotPath, gotAuth, gotUA string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.Contains(request.URL.Path, "loadCodeAssist"):
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, `{"cloudaicompanionProject":"proj-1","allowedTiers":[{"id":"free-tier","isDefault":true}]}`)
		case strings.Contains(request.URL.Path, "streamGenerateContent"):
			gotPath = request.URL.Path
			gotAuth = request.Header.Get("Authorization")
			gotUA = request.Header.Get("User-Agent")
			raw, _ := io.ReadAll(request.Body)
			_ = json.Unmarshal(raw, &gotBody)
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hi"},{"thought":true,"text":"think"},{"functionCall":{"id":"c1","name":"t","args":{"x":1}},"thoughtSignature":"sig"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7}}}`+"\n\n")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	original := agLoadAssistURL
	agLoadAssistURL = server.URL + "/v1internal:loadCodeAssist"
	t.Cleanup(func() { agLoadAssistURL = original })

	client, err := newHTTPClient(Provider{ID: "antigravity", Label: "Antigravity", Kind: KindAntigravity}, server.URL, "oauth-token")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	var text, reasoning strings.Builder
	var calls []ToolCall
	var usage Usage
	err = client.Stream(context.Background(), ChatRequest{
		Model: "gemini-3.8-flash", System: "sys",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
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

	if gotPath != "/v1internal:streamGenerateContent" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer oauth-token" || gotUA != agUserAgent {
		t.Errorf("headers: auth=%q ua=%q", gotAuth, gotUA)
	}
	if gotBody["project"] != "proj-1" || gotBody["model"] != "gemini-3.8-flash" {
		t.Errorf("body = %v", gotBody)
	}
	request, _ := gotBody["request"].(map[string]any)
	if request == nil || request["systemInstruction"] == nil {
		t.Errorf("the envelope should carry the system instruction: %v", gotBody)
	}
	if text.String() != "hi" || reasoning.String() != "think" {
		t.Errorf("text=%q reasoning=%q", text.String(), reasoning.String())
	}
	if len(calls) != 1 || calls[0].Name != "t" || !strings.Contains(calls[0].Arguments, `"x":1`) {
		t.Errorf("calls = %+v", calls)
	}
	if usage.TotalTokens != 7 {
		t.Errorf("usage = %+v", usage)
	}
}

// TestAntigravityModelsUseUpstreamIDs pins the wire ids the Cloud Code backend
// expects. The catalogue id is short and readable; the API id carries the
// thinking tier, and a mismatch is a 404 "check the model id and base URL".
func TestAntigravityModelsUseUpstreamIDs(t *testing.T) {
	cases := map[string]string{
		"antigravity-gemini-3.8-flash":  "gemini-3.8-flash-medium",
		"antigravity-gemini-3.5-flash":  "gemini-3.5-flash-low",
		"antigravity-gemini-pro":        "gemini-pro-agent",
		"antigravity-claude-opus-5-5":   "claude-opus-5-5-high",
		"antigravity-claude-sonnet-5-5": "claude-sonnet-5-5-medium",
	}
	for id, want := range cases {
		model, ok := ModelByID(id)
		if !ok {
			t.Errorf("model %q is missing", id)
			continue
		}
		if got := model.WireID(); got != want {
			t.Errorf("%s wire id = %q, want %q", id, got, want)
		}
	}
}

// TestAntigravitySurfacesAnEmptyFinishReason covers a stream that ends with no
// content for a block reason: the client must name the reason instead of
// returning an empty step the agent would retry into the same empty answer.
func TestAntigravitySurfacesAnEmptyFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.URL.Path, "streamGenerateContent") {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, `data: {"response":{"candidates":[{"content":{"parts":[]},"finishReason":"SAFETY"}]}}`+"\n\n")
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "antigravity", Label: "Antigravity", Kind: KindAntigravity}, server.URL, "oauth-token")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	client.(*antigravityClient).SetAccountID("proj-1")
	err = client.Stream(context.Background(), ChatRequest{
		Model:    "gemini-3.8-flash",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("an empty SAFETY finish must surface an error")
	}
	if !strings.Contains(err.Error(), "safety") {
		t.Errorf("error = %v, want the finish reason named", err)
	}
}

func TestSanitizeFunctionName(t *testing.T) {
	if got := sanitizeFunctionName("weird name!"); got != "weird_name_" {
		t.Errorf("sanitizeFunctionName = %q", got)
	}
	if got := sanitizeFunctionName("9lives"); got != "_9lives" {
		t.Errorf("sanitizeFunctionName = %q, want a leading underscore", got)
	}
}

func TestCleanAntigravitySchemaDropsUnsupportedKeys(t *testing.T) {
	cleaned := cleanAntigravitySchema(map[string]any{
		"type": "object", "$schema": "x", "additionalProperties": false,
		"properties": map[string]any{"a": map[string]any{"type": "string", "default": "z"}},
	})
	if _, ok := cleaned["$schema"]; ok {
		t.Errorf("$schema should be dropped")
	}
	properties := cleaned["properties"].(map[string]any)
	entry := properties["a"].(map[string]any)
	if _, ok := entry["default"]; ok {
		t.Errorf("default should be dropped")
	}
}

// TestCleanAntigravitySchemaDropsPropertyNames covers the 400 an MCP tool
// caused: its schema carried propertyNames, which the Gemini API rejects with
// "Unknown name propertyNames".
// TestCleanAntigravitySchemaPrunesUndefinedRequired covers the live 400
// "property is not defined": a required name with no matching property, at the
// top level and inside an array item, must be dropped.
func TestCleanAntigravitySchemaPrunesUndefinedRequired(t *testing.T) {
	cleaned := cleanAntigravitySchema(map[string]any{
		"type": "object",
		"properties": map[string]any{"todos": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":       "object",
				"properties": map[string]any{"title": map[string]any{"type": "string"}},
				"required":   []string{"title", "status"},
			},
		}},
		"required": []string{"todos", "ghost"},
	})
	if got := cleaned["required"]; !reflect.DeepEqual(got, []string{"todos"}) {
		t.Errorf("required = %#v, want [todos]", got)
	}
	items := cleaned["properties"].(map[string]any)["todos"].(map[string]any)["items"].(map[string]any)
	if got := items["required"]; !reflect.DeepEqual(got, []string{"title"}) {
		t.Errorf("items required = %#v, want [title]", got)
	}
}

// TestCleanAntigravitySchemaCollapsesTypeUnion covers the live 400 "Proto field
// is not repeating": a JSON Schema type union must become one type plus
// nullable.
func TestCleanAntigravitySchemaCollapsesTypeUnion(t *testing.T) {
	cleaned := cleanAntigravitySchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": []any{"string", "null"}},
			"counts": map[string]any{"type": []string{"integer"}},
		},
	})
	properties := cleaned["properties"].(map[string]any)
	path := properties["path"].(map[string]any)
	if path["type"] != "string" || path["nullable"] != true {
		t.Errorf("path = %#v, want type string and nullable true", path)
	}
	counts := properties["counts"].(map[string]any)
	if counts["type"] != "integer" {
		t.Errorf("counts type = %#v, want integer", counts["type"])
	}
	if _, present := counts["nullable"]; present {
		t.Errorf("counts must not be nullable")
	}
}

func TestCleanAntigravitySchemaDropsPropertyNames(t *testing.T) {
	cleaned := cleanAntigravitySchema(map[string]any{
		"type": "object",
		"properties": map[string]any{"env": map[string]any{
			"type":                 "object",
			"propertyNames":        map[string]any{"pattern": "^[A-Z_]+$"},
			"additionalProperties": map[string]any{"type": "string"},
		}},
	})
	env := cleaned["properties"].(map[string]any)["env"].(map[string]any)
	if _, ok := env["propertyNames"]; ok {
		t.Errorf("propertyNames should be dropped")
	}
	if _, ok := env["additionalProperties"]; ok {
		t.Errorf("additionalProperties should be dropped")
	}
}

// TestCleanAntigravitySchemaStringifiesNumericEnums covers the live 400
// "Invalid value ... (TYPE_STRING), 90": the pdf-tool-mcp pdf_rotate schema
// declared a numeric enum, which the Gemini function schema rejects because
// its enum field is repeated string.
func TestCleanAntigravitySchemaStringifiesNumericEnums(t *testing.T) {
	cleaned := cleanAntigravitySchema(map[string]any{
		"type": "object",
		"properties": map[string]any{"angle": map[string]any{
			"type": "number", "enum": []any{90.0, 180.0, 270.0},
		}},
	})
	angle := cleaned["properties"].(map[string]any)["angle"].(map[string]any)
	if angle["type"] != "string" {
		t.Errorf("angle type = %#v, want string", angle["type"])
	}
	if got := angle["enum"]; !reflect.DeepEqual(got, []string{"90", "180", "270"}) {
		t.Errorf("angle enum = %#v, want [90 180 270]", got)
	}
}

func TestCleanAntigravitySchemaStripsExclusiveBoundsAndNonArrayItems(t *testing.T) {
	cleaned := cleanAntigravitySchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit": map[string]any{
				"type":             "number",
				"exclusiveMinimum": 0,
				"exclusiveMaximum": 100,
				"multipleOf":       5,
				"items":            map[string]any{"type": "string"},
			},
			"tags": map[string]any{
				"type":  "array",
				"items": map[string]any{},
			},
		},
	})
	props := cleaned["properties"].(map[string]any)
	limit := props["limit"].(map[string]any)
	if _, ok := limit["exclusiveMinimum"]; ok {
		t.Errorf("exclusiveMinimum should be stripped")
	}
	if _, ok := limit["exclusiveMaximum"]; ok {
		t.Errorf("exclusiveMaximum should be stripped")
	}
	if _, ok := limit["multipleOf"]; ok {
		t.Errorf("multipleOf should be stripped")
	}
	if _, ok := limit["items"]; ok {
		t.Errorf("items should be stripped from non-array")
	}
	tags := props["tags"].(map[string]any)
	tagsItems := tags["items"].(map[string]any)
	if tagsItems["type"] != "string" {
		t.Errorf("empty items on array should default to string type, got %#v", tagsItems)
	}
}

func TestAntigravitySessionIDIsNonNegative(t *testing.T) {
	client := &antigravityClient{httpClient: &httpClient{}}
	for i := 0; i < 50; i++ {
		client.sessionID = ""
		sess := client.session()
		if strings.HasPrefix(sess, "-") {
			t.Fatalf("session ID must not be negative, got %s", sess)
		}
	}
}

func TestAntigravityAgProjectIDVariants(t *testing.T) {
	// String with projects/ prefix
	if got := agProjectID(json.RawMessage(`"projects/my-gcp-project"`)); got != "my-gcp-project" {
		t.Errorf("agProjectID(string) = %q, want my-gcp-project", got)
	}

	// Bare string
	if got := agProjectID(json.RawMessage(`"bare-project"`)); got != "bare-project" {
		t.Errorf("agProjectID(bare) = %q, want bare-project", got)
	}

	// Object with projectId
	if got := agProjectID(json.RawMessage(`{"projectId":"proj-from-obj"}`)); got != "proj-from-obj" {
		t.Errorf("agProjectID(projectId) = %q, want proj-from-obj", got)
	}

	// Object with name (projects/...)
	if got := agProjectID(json.RawMessage(`{"name":"projects/proj-name"}`)); got != "proj-name" {
		t.Errorf("agProjectID(name) = %q, want proj-name", got)
	}
}

func TestAntigravityRetriesOn401WithRefreshedKey(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.Contains(request.URL.Path, "loadCodeAssist"):
			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, `{"cloudaicompanionProject":"proj-1"}`)
		case strings.Contains(request.URL.Path, "streamGenerateContent"):
			attempts++
			auth := request.Header.Get("Authorization")
			if attempts == 1 {
				// Simulate token expired on first attempt
				http.Error(writer, `{"error":{"code":401,"message":"Request had invalid authentication credentials. Expected OAuth 2 access token."}}`, http.StatusUnauthorized)
				return
			}
			if auth != "Bearer fresh-refreshed-token" {
				t.Errorf("second attempt Authorization = %q, want Bearer fresh-refreshed-token", auth)
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, `data: {"response":{"candidates":[{"content":{"parts":[{"text":"recovered!"}]}}]}}`+"\n\n")
		}
	}))
	defer server.Close()

	original := agLoadAssistURL
	agLoadAssistURL = server.URL + "/v1internal:loadCodeAssist"
	t.Cleanup(func() { agLoadAssistURL = original })

	rawClient, err := newHTTPClient(Provider{ID: "antigravity", Label: "Antigravity", Kind: KindAntigravity}, server.URL, "stale-token")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}

	client := rawClient.(*antigravityClient)
	client.SetForceKeyResolver(func(id string) string {
		return "fresh-refreshed-token"
	})

	var answer strings.Builder
	err = client.Stream(context.Background(), ChatRequest{
		Model:    "gemini-3.8-flash",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(event StreamEvent) error {
		if event.Type == EventTextDelta {
			answer.WriteString(event.Text)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("Stream failed after 401 retry: %v", err)
	}
	if answer.String() != "recovered!" {
		t.Errorf("answer = %q, want recovered!", answer.String())
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

// antigravityToolLoop is a history the agent loop actually produces: one user
// turn, a step that called three tools at once, their results, and a second
// step that called one more. The shared list holds one message per result, so
// this is the shape the encoder has to fold back into Gemini's contract.
func antigravityToolLoop() []Message {
	return []Message{
		{Role: RoleUser, Content: "selamat siang"},
		{Role: RoleAssistant, Content: "I will look around.", ToolCalls: []ToolCall{
			{ID: "call_1", Name: "read_file", Arguments: `{"path":"main.go"}`},
			{ID: "call_2", Name: "glob", Arguments: `{"pattern":"**/*.go"}`},
			{ID: "call_3", Name: "list_directory", Arguments: `{"path":"."}`},
		}},
		{Role: RoleTool, ToolID: "call_1", Name: "read_file", Content: "package main"},
		{Role: RoleTool, ToolID: "call_2", Name: "glob", Content: "main.go"},
		{Role: RoleTool, ToolID: "call_3", Name: "list_directory", Content: "cmd/"},
		{Role: RoleAssistant, Content: "Now editing.", ToolCalls: []ToolCall{
			{ID: "call_4", Name: "edit", Arguments: `{"path":"main.go"}`},
		}},
		{Role: RoleTool, ToolID: "call_4", Name: "edit", Content: "edited"},
	}
}

// checkAntigravityTurnOrder walks encoded contents and reports the first place
// the Gemini function-calling contract is broken. The backend answers such a
// history with 400 "Please ensure that function call turn comes immediately
// after a user turn or after a function response turn", which is the error
// this pins.
func checkAntigravityTurnOrder(t *testing.T, contents []map[string]any) {
	t.Helper()
	if len(contents) == 0 {
		t.Fatalf("encodeContents returned no contents")
	}
	for index, turn := range contents {
		role, _ := turn["role"].(string)
		parts, _ := turn["parts"].([]map[string]any)
		if role != "user" && role != "model" {
			t.Fatalf("turn %d has role %q, want user or model", index, role)
		}
		if len(parts) == 0 {
			t.Fatalf("turn %d (%s) carries no parts: a dropped turn is what leaves a functionCall unanchored", index, role)
		}
		for _, part := range parts {
			if _, ok := part["functionCall"]; !ok {
				continue
			}
			// A functionCall turn must follow a user turn or a function
			// response turn, never another model turn.
			if index == 0 {
				t.Fatalf("turn 0 carries a functionCall with no turn before it")
			}
			previousRole, _ := contents[index-1]["role"].(string)
			if previousRole != "user" {
				t.Fatalf("turn %d carries a functionCall after a %s turn", index, previousRole)
			}
			// And it must be answered by the immediately following turn.
			if index+1 >= len(contents) {
				t.Fatalf("turn %d carries a functionCall that no turn answers", index)
			}
			answerRole, _ := contents[index+1]["role"].(string)
			if answerRole != "user" {
				t.Fatalf("turn %d carries a functionCall answered by a %s turn", index, answerRole)
			}
		}
	}
}

func TestAntigravityFoldsToolResultsIntoOneUserTurn(t *testing.T) {
	client := &antigravityClient{httpClient: &httpClient{}}
	contents := client.encodeContents(ChatRequest{Messages: antigravityToolLoop()})
	checkAntigravityTurnOrder(t, contents)

	// user, model(3 calls), user(3 answers), model(1 call), user(1 answer).
	want := []string{"user", "model", "user", "model", "user"}
	if len(contents) != len(want) {
		t.Fatalf("turn count = %d, want %d: %#v", len(contents), len(want), contents)
	}
	for index, role := range want {
		if got, _ := contents[index]["role"].(string); got != role {
			t.Fatalf("turn %d role = %q, want %q", index, got, role)
		}
	}

	answers, _ := contents[2]["parts"].([]map[string]any)
	if len(answers) != 3 {
		t.Fatalf("the answering turn carries %d parts, want one functionResponse per call", len(answers))
	}
	for index, part := range answers {
		response, ok := part["functionResponse"].(map[string]any)
		if !ok {
			t.Fatalf("part %d = %#v, want a functionResponse", index, part)
		}
		if response["name"] == "" || response["name"] == nil {
			t.Fatalf("part %d names no call: %#v", index, response)
		}
	}
}

// TestAntigravityKeepsATurnThatEncodesToNothing pins the other half of the
// rule: dropping a user turn from between two model turns puts a functionCall
// straight after another model turn, which is the same 400.
func TestAntigravityKeepsATurnThatEncodesToNothing(t *testing.T) {
	client := &antigravityClient{httpClient: &httpClient{}}
	contents := client.encodeContents(ChatRequest{Messages: []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: "{}"}}},
		{Role: RoleTool, ToolID: "c1", Name: "read_file", Content: "out"},
		{Role: RoleUser, Content: "   "},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "edit", Arguments: "{}"}}},
		{Role: RoleTool, ToolID: "c2", Name: "edit", Content: "done"},
	}})
	checkAntigravityTurnOrder(t, contents)
	if len(contents) != 6 {
		t.Fatalf("turn count = %d, want 6: the blank user turn must survive as a placeholder", len(contents))
	}
	if parts, _ := contents[3]["parts"].([]map[string]any); len(parts) != 1 {
		t.Fatalf("placeholder turn parts = %#v, want one", parts)
	}
}

// TestAntigravityFoldsAnImageTurnIntoTheAnsweringTurn covers a tool that
// produced an image: the agent follows the result with a user turn carrying
// the attachment, and that turn must not be split away from the answers.
func TestAntigravityFoldsAnImageTurnIntoTheAnsweringTurn(t *testing.T) {
	client := &antigravityClient{httpClient: &httpClient{}}
	contents := client.encodeContents(ChatRequest{Messages: []Message{
		{Role: RoleUser, Content: "show me main.go"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_image", Arguments: "{}"}}},
		{Role: RoleTool, ToolID: "c1", Name: "read_image", Content: "read the image"},
		{Role: RoleUser, Content: "Here is the image.", Images: []Image{{MediaType: "image/png", Data: "AAAA"}}},
		{Role: RoleAssistant, Content: "I see a function."},
	}})
	checkAntigravityTurnOrder(t, contents)

	// Google Cloud Code forbids mixing functionResponse and user prompt text/images
	// in the same turn. The functionResponse must stand in its own user turn,
	// followed by the user prompt turn with the image.
	want := []string{"user", "model", "user", "user", "model"}
	if len(contents) != len(want) {
		t.Fatalf("turn count = %d, want %d: %#v", len(contents), len(want), contents)
	}
	fnParts, _ := contents[2]["parts"].([]map[string]any)
	if _, ok := fnParts[0]["functionResponse"]; !ok {
		t.Fatalf("first part = %#v, want the functionResponse", fnParts[0])
	}
	userParts, _ := contents[3]["parts"].([]map[string]any)
	if _, ok := userParts[1]["inlineData"]; !ok {
		t.Fatalf("last part = %#v, want the image", userParts[1])
	}
}

func TestAntigravityInterruptedCallFollowedByUserMessage(t *testing.T) {
	client := &antigravityClient{httpClient: &httpClient{}}
	contents := client.encodeContents(ChatRequest{Messages: []Message{
		{Role: RoleUser, Content: "build installer"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "run_wait", Arguments: "{}"}}},
		{Role: RoleTool, ToolID: "call_1", Name: "run_wait", Content: "The process restarted while this call was in flight. (recovered after a restart)"},
		{Role: RoleUser, Content: "selamat siang"},
	}})
	checkAntigravityTurnOrder(t, contents)

	// contents[2] must be functionResponse ONLY
	fnParts, _ := contents[2]["parts"].([]map[string]any)
	if _, ok := fnParts[0]["functionResponse"]; !ok {
		t.Fatalf("expected functionResponse in turn 2, got: %#v", fnParts)
	}
	// contents[3] must be the user turn with "selamat siang"
	userParts, _ := contents[3]["parts"].([]map[string]any)
	if text, _ := userParts[0]["text"].(string); text != "selamat siang" {
		t.Fatalf("expected user text in turn 3, got: %#v", userParts)
	}
}

func TestAntigravityEncodesAnEmptyToolResult(t *testing.T) {
	client := &antigravityClient{httpClient: &httpClient{}}
	contents := client.encodeContents(ChatRequest{Messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "run_command", Arguments: "{}"}}},
		{Role: RoleTool, ToolID: "c1", Name: "run_command", Content: ""},
	}})
	checkAntigravityTurnOrder(t, contents)
	parts, _ := contents[len(contents)-1]["parts"].([]map[string]any)
	response, _ := parts[0]["functionResponse"].(map[string]any)
	body, _ := response["response"].(map[string]any)
	if body["result"] == "" || body["result"] == nil {
		t.Fatalf("an empty result reads as an unanswered call: %#v", response)
	}
}

// TestAntigravityAnchorsAFunctionCallThatOpensTheHistory covers the shape
// Compact can leave behind: it refuses to put a tool result at the head, so a
// drop may take the user turn and leave the assistant's call standing first.
// The backend rejects a history that opens with a functionCall, so a
// continuation turn has to go in front of the pair rather than the pair being
// dropped.
func TestAntigravityAnchorsAFunctionCallThatOpensTheHistory(t *testing.T) {
	client := &antigravityClient{httpClient: &httpClient{}}
	contents := client.encodeContents(ChatRequest{Messages: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"main.go"}`}}},
		{Role: RoleTool, ToolID: "c1", Name: "read_file", Content: "package main"},
		{Role: RoleAssistant, Content: "It builds."},
	}})
	checkAntigravityTurnOrder(t, contents)

	want := []string{"user", "model", "user", "model"}
	if len(contents) != len(want) {
		t.Fatalf("turn count = %d, want %d: %#v", len(contents), len(want), contents)
	}
	for index, role := range want {
		if got, _ := contents[index]["role"].(string); got != role {
			t.Fatalf("turn %d role = %q, want %q", index, got, role)
		}
	}
	// The call and its answer survive the repair, not just the anchoring turn.
	callParts, _ := contents[1]["parts"].([]map[string]any)
	if _, ok := callParts[0]["functionCall"]; !ok {
		t.Fatalf("the call was lost in the repair: %#v", callParts)
	}
	answerParts, _ := contents[2]["parts"].([]map[string]any)
	if _, ok := answerParts[0]["functionResponse"]; !ok {
		t.Fatalf("the answer was lost in the repair: %#v", answerParts)
	}
}

// TestAntigravityLeavesAWellFormedHistoryUntouched is the guard against the
// repair above firing on a history that needs none: a continuation turn the
// backend did not ask for is a turn the model has to explain to itself.
func TestAntigravityLeavesAWellFormedHistoryUntouched(t *testing.T) {
	client := &antigravityClient{httpClient: &httpClient{}}
	contents := client.encodeContents(ChatRequest{Messages: antigravityToolLoop()})
	for index, turn := range contents {
		parts, _ := turn["parts"].([]map[string]any)
		for _, part := range parts {
			text, _ := part["text"].(string)
			if text == "(continued)" {
				t.Fatalf("turn %d gained a synthetic continuation turn: %#v", index, turn)
			}
		}
	}
}
