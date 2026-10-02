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
		"antigravity-claude-sonnet-4-6": "claude-sonnet-4-6",
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
