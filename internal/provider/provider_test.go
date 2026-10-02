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
	// Pick a real entry rather than a fixed id, so a catalogue update does not
	// break the test that guards the resolver.
	target := pickModel(t, "an anthropic model", func(m Model) bool { return m.Provider == "anthropic" })

	if model, ok := FindModel(target.ID); !ok || model.Provider != "anthropic" {
		t.Errorf("exact id %q did not resolve: %+v ok=%v", target.ID, model, ok)
	}
	if model, ok := FindModel(target.Label); !ok || model.ID != target.ID {
		t.Errorf("label %q did not resolve: %+v ok=%v", target.Label, model, ok)
	}
	if model, ok := FindModel("deepseek"); ok {
		// "deepseek" matches several models, so it must not resolve to one.
		t.Errorf("an ambiguous query should not resolve, got %+v", model)
	}
	// An empty query resolves nothing rather than everything.
	if _, ok := FindModel("   "); ok {
		t.Errorf("a blank query must not resolve")
	}
}

// TestPlanProvidersHaveTheirOwnHost pins the two plan endpoints as first-class
// providers, so an operator does not have to hand-configure an OpenAI
// compatible endpoint for them.
func TestPlanProvidersHaveTheirOwnHost(t *testing.T) {
	want := map[string]string{
		"stepfun-plan":    "https://api.stepfun.ai/step_plan/v1",
		"qwen-token-plan": "https://token-plan.maas.qwencloudapi.com/compatible-mode/v1",
	}
	for id, base := range want {
		info, ok := ByID(id)
		if !ok {
			t.Fatalf("provider %q is missing", id)
		}
		if info.DefaultBaseURL != base {
			t.Errorf("%s base URL = %q, want %q", id, info.DefaultBaseURL, base)
		}
		if info.Kind != KindOpenAI {
			t.Errorf("%s kind = %q, want openai", id, info.Kind)
		}
		if NeedsEndpoint(id) {
			t.Errorf("%s should have a default endpoint", id)
		}
		if got := DefaultBaseURL(id); got != base {
			t.Errorf("DefaultBaseURL(%s) = %q, want %q", id, got, base)
		}
		if models := ModelsFor(id); len(models) == 0 {
			t.Errorf("%s has no catalogue models", id)
		}
	}
}

// TestQwenTokenPlanListsThePlannedModels pins the model list the operator
// asked for, so a catalogue edit cannot quietly drop one.
func TestQwenTokenPlanListsThePlannedModels(t *testing.T) {
	want := []string{
		"qwen3.8-max", "qwen3.8-27b", "qwen3.7-max",
		"qwen3.8-flash", "qwen3.6-flash",
		"deepseek-v4.1-flash",
		"deepseek-v4-pro-0813", "deepseek-v4-pro",
		"deepseek-v4-flash-0731",
		"glm-5.3", "glm-5.2",
	}
	models := ModelsFor("qwen-token-plan")
	have := make(map[string]bool, len(models))
	for _, model := range models {
		have[model.WireID()] = true
	}
	for _, id := range want {
		if !have[id] {
			t.Errorf("qwen-token-plan is missing model %q", id)
		}
	}
}

// TestPlanModelIdsResolveWithoutAmbiguity keeps the host-prefixed ids unique:
// two catalogue entries sharing an id would make ModelByID pick one silently.
func TestPlanModelIdsResolveWithoutAmbiguity(t *testing.T) {
	seen := map[string]bool{}
	for _, model := range Models() {
		if seen[model.ID] {
			t.Errorf("duplicate catalogue id %q", model.ID)
		}
		seen[model.ID] = true
	}
	for _, id := range []string{
		"stepfun-plan/step-3.7-flash",
		"qwen-token-plan/qwen3.8-max",
	} {
		model, ok := ModelByID(id)
		if !ok {
			t.Fatalf("ModelByID(%q) failed", id)
		}
		if model.WireID() == "" {
			t.Errorf("%s has an empty wire id", id)
		}
		if model.Window() <= 0 {
			t.Errorf("%s reported no context window", id)
		}
	}
	if model, ok := ModelFromQuery("stepfun-plan:step-3.7-flash"); !ok || model.Provider != "stepfun-plan" {
		t.Errorf("provider-qualified plan model did not resolve: %+v ok=%v", model, ok)
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
		// An OAuth provider has no environment variable: its credential comes
		// from a stored login, not an API key.
		if info.OAuth {
			continue
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
		"propertyNames":        map[string]any{"pattern": "^[a-z]+$"},
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "additionalProperties": false},
		},
	}
	clean := sanitizeGoogleSchema(schema)
	if _, present := clean["additionalProperties"]; present {
		t.Errorf("additionalProperties must be removed")
	}
	if _, present := clean["propertyNames"]; present {
		t.Errorf("propertyNames must be removed")
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

// TestCatalogueDropsDeadCodexModelsAndFixesHaiku pins two model-id fixes: the
// Codex models the backend no longer serves, and the dated Claude Haiku id.
func TestCatalogueDropsDeadCodexModelsAndFixesHaiku(t *testing.T) {
	for _, id := range []string{"codex-gpt-5.4", "codex-gpt-5.4-codex", "codex-gpt-5.3-codex-spark"} {
		if _, ok := ModelByID(id); ok {
			t.Errorf("%s returns 400 model is not supported and must not be offered", id)
		}
	}
	haiku, ok := ModelByID("claude-oauth-haiku-4-5")
	if !ok {
		t.Fatal("claude-oauth-haiku-4-5 is missing")
	}
	if haiku.WireID() != "claude-haiku-4-5-20251001" {
		t.Errorf("haiku wire id = %q, want claude-haiku-4-5-20251001", haiku.WireID())
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
