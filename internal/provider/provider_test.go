package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestToolCallAccumulatorRebuildsFragments(t *testing.T) {
	accumulator := newToolCallAccumulator()
	accumulator.add(openAIToolDelta{Index: 0, ID: "call_1", Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: "read_file", Arguments: `{"pa`}})
	accumulator.add(openAIToolDelta{Index: 0, Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Arguments: `th":"a.go"}`}})

	calls := accumulator.finish()
	if len(calls) != 1 {
		t.Fatalf("expected one call, got %d", len(calls))
	}
	if calls[0].Name != "read_file" || calls[0].ID != "call_1" {
		t.Errorf("call identity is wrong: %+v", calls[0])
	}
	if calls[0].Arguments != `{"path":"a.go"}` {
		t.Errorf("arguments = %q, want the concatenated JSON", calls[0].Arguments)
	}
}

func TestToolCallAccumulatorNormalises(t *testing.T) {
	accumulator := newToolCallAccumulator()
	accumulator.add(openAIToolDelta{Index: 3, Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: "grep"}})
	calls := accumulator.finish()
	if calls[0].ID != "call_3" {
		t.Errorf("a missing id should be synthesised, got %q", calls[0].ID)
	}
	if calls[0].Arguments != "{}" {
		t.Errorf("empty arguments should become {}, got %q", calls[0].Arguments)
	}
}

func TestToolCallAccumulatorSkipsNamelessFragments(t *testing.T) {
	accumulator := newToolCallAccumulator()
	accumulator.add(openAIToolDelta{Index: 0, Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Arguments: "{}"}})
	if calls := accumulator.finish(); len(calls) != 0 {
		t.Fatalf("a call without a name cannot be executed, got %+v", calls)
	}
}

func TestDecodeArguments(t *testing.T) {
	if decoded, err := decodeArguments(""); err != nil || len(decoded) != 0 {
		t.Errorf("empty arguments should decode to an empty map, got %v err=%v", decoded, err)
	}
	decoded, err := decodeArguments(`{"path":"x","limit":5}`)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["path"] != "x" {
		t.Errorf("path = %v", decoded["path"])
	}
	if _, err := decodeArguments(`[1,2]`); err == nil {
		t.Errorf("a JSON array is not a valid argument object")
	}
}

func TestFindModelResolvesIdLabelAndSubstring(t *testing.T) {
	if model, ok := FindModel("claude-sonnet-4-5"); !ok || model.Provider != "anthropic" {
		t.Errorf("exact id did not resolve: %+v ok=%v", model, ok)
	}
	if model, ok := FindModel("GPT-5.6 Sol"); !ok || model.ID != "gpt-5.6" {
		t.Errorf("label did not resolve: %+v ok=%v", model, ok)
	}
	if model, ok := FindModel("deepseek"); ok {
		// "deepseek" matches two models, so it must not resolve to one.
		t.Errorf("an ambiguous query should not resolve, got %+v", model)
	}
}

func TestProvidersRequiringKeySortedAndConsistent(t *testing.T) {
	providers := ProvidersRequiringKey()
	if len(providers) == 0 {
		t.Fatal("expected key-based providers")
	}
	for index := 1; index < len(providers); index++ {
		if providers[index-1].Label > providers[index].Label {
			t.Fatalf("providers must be sorted by label")
		}
	}
	for _, info := range providers {
		if !info.NeedsKey {
			t.Errorf("%s is in the key list but does not need a key", info.ID)
		}
		if len(info.EnvKeys) == 0 {
			t.Errorf("%s needs a key but declares no environment variable", info.ID)
		}
	}
}

func TestBaseURLForFallsBackForCompatible(t *testing.T) {
	cfg := config.Default()
	if got := BaseURLFor(cfg, "openai-compatible"); got != "https://api.openai.com/v1" {
		t.Errorf("openai-compatible should fall back to the OpenAI base, got %q", got)
	}
	if got := BaseURLFor(cfg, "ollama"); got != "http://localhost:11434/v1" {
		t.Errorf("ollama base = %q", got)
	}
}

func TestSanitizeGoogleSchemaDropsUnsupportedKeywords(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "additionalProperties": false},
		},
	}
	clean := sanitizeGoogleSchema(schema)
	if _, present := clean["additionalProperties"]; present {
		t.Errorf("additionalProperties must be removed")
	}
	if _, present := clean["$schema"]; present {
		t.Errorf("$schema must be removed")
	}
	nested, ok := clean["properties"].(map[string]any)["path"].(map[string]any)
	if !ok {
		t.Fatalf("nested properties were lost: %+v", clean)
	}
	if _, present := nested["additionalProperties"]; present {
		t.Errorf("nested additionalProperties must be removed")
	}
}

func TestEncodeOpenAIToolsShape(t *testing.T) {
	tools := []ToolDef{{Name: "read_file", Description: "Read a file.", Schema: map[string]any{"type": "object"}}}
	encoded := encodeOpenAITools(tools)
	raw, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"type":"function"`, `"name":"read_file"`, `"parameters"`} {
		if !strings.Contains(text, want) {
			t.Errorf("encoded tool is missing %s: %s", want, text)
		}
	}
}
