package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// TestAntigravityRequestMatchesTheCLI pins what the client sends to Google
// against the request the installed agy 1.5.15 was captured sending: the same
// header set, the same envelope fields and the same thinking spelling. A
// header or field the CLI does not send is the kind of difference that gets an
// account limited, so the absences are asserted as firmly as the presences.
func TestAntigravityRequestMatchesTheCLI(t *testing.T) {
	var streamHeaders http.Header
	var streamBody map[string]any
	var loadBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		switch {
		case strings.Contains(request.URL.Path, "loadCodeAssist"):
			_ = json.Unmarshal(raw, &loadBody)
			fmt.Fprint(writer, `{"cloudaicompanionProject":"proj-1","allowedTiers":[{"id":"free-tier","isDefault":true}]}`)
		case strings.Contains(request.URL.Path, "streamGenerateContent"):
			streamHeaders = request.Header.Clone()
			_ = json.Unmarshal(raw, &streamBody)
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}]}}`+"\n\n")
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
	err = client.Stream(context.Background(), ChatRequest{
		Model:    "gemini-4-argon-medium",
		Effort:   EffortHigh,
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
		Tools: []ToolDef{{Name: "read_file", Description: "read", Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}},
		}}},
	}, func(StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// The CLI's account call names only its IDE type.
	metadata, _ := loadBody["metadata"].(map[string]any)
	if len(loadBody) != 1 || len(metadata) != 1 || metadata["ideType"] != "ANTIGRAVITY" {
		t.Errorf("loadCodeAssist body = %v, want only metadata.ideType ANTIGRAVITY", loadBody)
	}

	// Headers: the token, the JSON type and the CLI User-Agent. No session
	// header, no client-name pair and no event-stream Accept.
	if streamHeaders.Get("User-Agent") != agUserAgent() {
		t.Errorf("User-Agent = %q, want %q", streamHeaders.Get("User-Agent"), agUserAgent())
	}
	for _, name := range []string{"X-Machine-Session-Id", "X-Client-Name", "X-Client-Version", "Accept"} {
		if got := streamHeaders.Get(name); got != "" {
			t.Errorf("%s = %q, the CLI does not send it", name, got)
		}
	}

	if streamBody["requestType"] != "agent" || streamBody["userAgent"] != "antigravity" || streamBody["model"] != "gemini-4-argon-high" {
		t.Errorf("envelope = %v", streamBody)
	}
	uuid := `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`
	if id, _ := streamBody["requestId"].(string); !regexp.MustCompile(`^agent/` + uuid + `/\d{13}/` + uuid + `/1$`).MatchString(id) {
		t.Errorf("requestId = %q, want agent/<uuid>/<ms>/<uuid>/<step>", id)
	}
	request, _ := streamBody["request"].(map[string]any)
	if _, has := request["toolConfig"]; has {
		t.Errorf("the CLI sends no toolConfig, got %v", request["toolConfig"])
	}
	labels, _ := request["labels"].(map[string]any)
	for _, key := range []string{"cascade_id", "last_step_index", "request_id", "root_cascade_id", "trajectory_id", "used_claude", "used_claude_conservative", "used_non_gemini_model", "model_enum"} {
		if _, ok := labels[key]; !ok {
			t.Errorf("labels lack %q: %v", key, labels)
		}
	}
	if labels["model_enum"] != "MODEL_PLACEHOLDER_M417" || labels["last_step_index"] != "0" || labels["used_claude"] != "false" {
		t.Errorf("labels = %v", labels)
	}
	config, _ := request["generationConfig"].(map[string]any)
	thinking, _ := config["thinkingConfig"].(map[string]any)
	if config["maxOutputTokens"] != float64(65536) || thinking["thinkingLevel"] != "HIGH" || thinking["includeThoughts"] != true {
		t.Errorf("generationConfig = %v", config)
	}
	tools, _ := request["tools"].([]any)
	declarations, _ := tools[0].(map[string]any)["functionDeclarations"].([]any)
	parameters, _ := declarations[0].(map[string]any)["parameters"].(map[string]any)
	properties, _ := parameters["properties"].(map[string]any)
	if parameters["type"] != "OBJECT" || properties["path"].(map[string]any)["type"] != "STRING" || properties["limit"].(map[string]any)["type"] != "INTEGER" {
		t.Errorf("tool schema types are not upper case: %v", parameters)
	}
}

// TestAntigravityThinkingLevelFollowsTheModel proves every effort a Gemini 4
// family offers reaches the wire as the model-name suffix, and that the
// thinking level beside it is the suffix again, never a second opinion.
func TestAntigravityThinkingLevelFollowsTheModel(t *testing.T) {
	cases := []struct {
		model, effort, wantModel, wantLevel string
	}{
		{"antigravity-gemini-4-neon", EffortLow, "gemini-4-neon-low", "LOW"},
		{"antigravity-gemini-4-neon", EffortMedium, "gemini-4-neon-medium", "MEDIUM"},
		{"antigravity-gemini-4-neon", EffortHigh, "gemini-4-neon-high", "HIGH"},
		{"antigravity-gemini-4-neon", EffortXHigh, "gemini-4-neon-xhigh", "HIGH"},
		{"antigravity-gemini-4-neon", EffortMax, "gemini-4-neon-xhigh", "HIGH"},
		{"antigravity-gemini-4-neon", EffortUltra, "gemini-4-neon-xhigh", "HIGH"},
		{"antigravity-gemini-4-argon", EffortUltra, "gemini-4-argon-high", "HIGH"},
		{"gemini-3.8-flash-medium", EffortLow, "gemini-3.8-flash-low", "LOW"},
		{"claude-opus-5-5-medium", EffortHigh, "claude-opus-5-5-high", "HIGH"},
	}
	for _, tc := range cases {
		wire := gemini4WireModel(tc.model, tc.effort)
		if wire != tc.wantModel {
			t.Errorf("wire model for %s at %s = %q, want %q", tc.model, tc.effort, wire, tc.wantModel)
		}
		config := agGenerationConfig(ChatRequest{Model: tc.model, Effort: tc.effort}, wire)
		thinking, _ := config["thinkingConfig"].(map[string]any)
		if thinking["thinkingLevel"] != tc.wantLevel {
			t.Errorf("thinkingLevel for %s = %v, want %s", wire, thinking["thinkingLevel"], tc.wantLevel)
		}
	}
}

func TestAntigravityNeonIsInTheCatalogue(t *testing.T) {
	for _, model := range Models() {
		if model.ID == "antigravity-gemini-4-neon" {
			if model.WireID() != "gemini-4-neon-high" || model.Window() != 1048576 {
				t.Errorf("neon entry = wire %q window %d", model.WireID(), model.Window())
			}
			return
		}
	}
	t.Fatal("antigravity-gemini-4-neon is missing from the catalogue")
}

// TestAntigravityConsumerAccountUsesTheSharedProject covers the account that
// Cloud Code enrols but assigns no project: the CLI names the shared consumer
// project, so the client must neither onboard nor invent an id.
func TestAntigravityConsumerAccountUsesTheSharedProject(t *testing.T) {
	var onboarded int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.Contains(request.URL.Path, "loadCodeAssist"):
			fmt.Fprint(writer, `{"currentTier":{"id":"free-tier","name":"Antigravity"},"allowedTiers":[{"id":"free-tier","isDefault":true}]}`)
		case strings.Contains(request.URL.Path, "onboardUser"):
			onboarded++
			http.Error(writer, "no", http.StatusInternalServerError)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	originalLoad, originalOnboard := agLoadAssistURL, agOnboardURL
	agLoadAssistURL = server.URL + "/v1internal:loadCodeAssist"
	agOnboardURL = server.URL + "/v1internal:onboardUser"
	t.Cleanup(func() { agLoadAssistURL, agOnboardURL = originalLoad, originalOnboard })

	client, err := newHTTPClient(Provider{ID: "antigravity", Label: "Antigravity", Kind: KindAntigravity}, server.URL, "tok")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	project, err := client.(*antigravityClient).ensureProject(context.Background())
	if err != nil || project != "aicode-consumers" {
		t.Fatalf("project = %q, err = %v, want aicode-consumers", project, err)
	}
	if onboarded != 0 {
		t.Errorf("an enrolled account was onboarded %d times", onboarded)
	}
}

// TestAntigravityQuotaWithPerModelBuckets covers the summary some accounts get:
// a remaining fraction per model and no 5-hour or weekly window.
func TestAntigravityQuotaWithPerModelBuckets(t *testing.T) {
	body := []byte(`{"groups":[{"buckets":[
		{"bucketId":"gemini-4-neon-high","displayName":"Gemini 4 Neon (High)","remainingFraction":1},
		{"bucketId":"gemini-4-argon-high","displayName":"Gemini 4 Argon (High)","remainingFraction":0.4},
		{"bucketId":"claude-opus-5-5-high","displayName":"Claude Opus 5.5 (High)","remainingFraction":0.9},
		{"bucketId":"gpt-oss-120b-medium","displayName":"GPT-OSS 120B (Medium)","remainingFraction":0.7}]}]}`)
	snap := parseAntigravityQuota(&QuotaSnapshot{Provider: "antigravity"}, body)
	if snap.Unavailable != "" || len(snap.Windows) != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
	gemini, claude := snap.Windows[0], snap.Windows[1]
	if gemini.Key != "gemini_models" || gemini.UsedPercent != 60 {
		t.Errorf("gemini window = %+v, want the tightest model (60%% used)", gemini)
	}
	if claude.Key != "claude_gpt_models" || claude.UsedPercent < 29.9 || claude.UsedPercent > 30.1 {
		t.Errorf("claude window = %+v, want the tightest of Claude and GPT (30%% used)", claude)
	}
}
