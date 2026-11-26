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
	"time"
)

// TestClaudeFingerprintMatchesTheClient pins the build fingerprint to a value
// the real client produced: Claude Code 2.1.325 sent cc_version=2.1.325.382
// for the prompt "say hi", captured on a local endpoint.
func TestClaudeFingerprintMatchesTheClient(t *testing.T) {
	if got := claudeFingerprint("say hi", "2.1.325"); got != "382" {
		t.Fatalf("claudeFingerprint(say hi, 2.1.325) = %q, want 382", got)
	}
	// A prompt shorter than the sampled positions pads with 0, as the client does.
	if got := claudeFingerprint("", "2.1.325"); len(got) != 3 {
		t.Fatalf("claudeFingerprint of an empty prompt = %q, want three hex characters", got)
	}
}

// TestClaudeFingerprintTextStripsTheEnvWrapper pins what the billing
// fingerprint is taken from: the bare prompt, not the wrappers the harness
// appends. The real client fingerprints only the prompt ("say hi" yields
// 382), while every harness turn carries an env block after it.
func TestClaudeFingerprintTextStripsTheEnvWrapper(t *testing.T) {
	content := "say hi\n\n<env>\nworkspace_root: C:\\work\nfolder_trust: trusted\n</env>"
	if got := claudeFingerprintText([]Message{{Role: RoleUser, Content: content}}); got != "say hi" {
		t.Errorf("fingerprint text = %q, want the bare prompt", got)
	}
	if got := claudeFingerprint("say hi", "2.1.325"); got != "382" {
		t.Errorf("claudeFingerprint = %q, want 382", got)
	}
}

func TestClaudeBillingHeaderLeadsTheSystemBlocks(t *testing.T) {
	blocks := withClaudeCodeIdentity("be brief", "say hi")
	if len(blocks) < 2 {
		t.Fatalf("blocks = %v", blocks)
	}
	want := "x-anthropic-billing-header: cc_version=" + resolveClaudeCLIVersion() + ".382; cc_entrypoint=cli;"
	if resolveClaudeCLIVersion() != "2.1.325" {
		want = "x-anthropic-billing-header: cc_version=" + resolveClaudeCLIVersion() + "." + claudeFingerprint("say hi", resolveClaudeCLIVersion()) + "; cc_entrypoint=cli;"
	}
	if blocks[0]["text"] != want {
		t.Errorf("billing block = %q, want %q", blocks[0]["text"], want)
	}
	if blocks[1]["text"] != claudeCodeIdentity {
		t.Errorf("identity block = %q", blocks[1]["text"])
	}
}

func TestClaudeOAuthBetaListMatchesTheClient(t *testing.T) {
	for _, flag := range []string{"claude-code-20250219", "oauth-2025-04-20", "interleaved-thinking-2025-05-14",
		"context-management-2025-06-27", "per-turn-control-2026-07-01",
		"mid-conversation-tool-changes-2026-07-01", "inline-tools-2026-09-15",
		"advisor-tool-2026-03-01", "thinking-token-count-2026-05-13",
		"advanced-tool-use-2025-11-20", "effort-2025-11-24", "extended-cache-ttl-2025-04-11"} {
		if !strings.Contains(claudeOAuthBeta, flag) {
			t.Errorf("beta list lacks %s: %s", flag, claudeOAuthBeta)
		}
	}
}

func TestUUIDV7ShapeAndOrder(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	first := uuidV7()
	time.Sleep(2 * time.Millisecond)
	second := uuidV7()
	for _, id := range []string{first, second} {
		if !pattern.MatchString(id) {
			t.Errorf("uuidV7() = %q, not a version 7 UUID", id)
		}
	}
	if first >= second {
		t.Errorf("uuidV7 is not time ordered: %s then %s", first, second)
	}
}

// TestCodexRequestCarriesTheCLIIdentity checks the headers and body fields a
// Codex client sends on a Responses call, as captured from codex 0.146.0.
func TestCodexRequestCarriesTheCLIIdentity(t *testing.T) {
	var headers http.Header
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		headers = request.Header.Clone()
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "openai-codex", Label: "Codex", Kind: KindOpenAI}, server.URL, "tok")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	err = client.Stream(context.Background(), ChatRequest{
		Model: "gpt-6.2-sol", Effort: EffortUltra, System: "be brief",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if headers.Get("Originator") != "codex_cli_rs" || headers.Get("Accept") != "text/event-stream" {
		t.Errorf("originator/accept = %q / %q", headers.Get("Originator"), headers.Get("Accept"))
	}
	session, thread := headers.Get("Session-Id"), headers.Get("Thread-Id")
	if session == "" || session != thread || headers.Get("X-Client-Request-Id") != session || headers.Get("X-Codex-Window-Id") != session+":0" {
		t.Errorf("session ids: session=%q thread=%q request=%q window=%q", session, thread, headers.Get("X-Client-Request-Id"), headers.Get("X-Codex-Window-Id"))
	}
	if headers.Get("session_id") != "" {
		t.Errorf("the underscore session_id header is the old spelling and must not be sent")
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(headers.Get("X-Codex-Turn-Metadata")), &meta); err != nil || meta["session_id"] != session || meta["request_kind"] != "turn" || meta["installation_id"] == "" {
		t.Errorf("X-Codex-Turn-Metadata = %q (%v)", headers.Get("X-Codex-Turn-Metadata"), err)
	}
	if headers.Get("X-Openai-Internal-Codex-Responses-Lite") != "true" {
		t.Errorf("a GPT-6.2 request must carry the responses-lite header")
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "ultra" {
		t.Errorf("reasoning = %v, want effort ultra for gpt-6.2-sol", reasoning)
	}
	if body["prompt_cache_key"] != session {
		t.Errorf("prompt_cache_key = %v, want the conversation id %q", body["prompt_cache_key"], session)
	}
}

func TestCodexLiteModelsCoverTheGPT6Line(t *testing.T) {
	for _, model := range []string{
		"gpt-6.2-sol", "gpt-6.2-astra", "gpt-6.1-terra", "gpt-6.1-sol",
		"gpt-6-astra", "gpt-6-sol", "gpt-6-luna",
		"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
		"gpt-reserve", "codex-auto-review",
		"gpt-daybreak-blue-latest", "gpt-daybreak-red-latest",
	} {
		if !isCodexResponsesLiteModel(model) {
			t.Errorf("%s should take the responses-lite shape", model)
		}
	}
	for _, model := range []string{
		"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex",
		"gpt-5.2-codex", "gpt-5.1-codex-max", "gpt-5.1-codex", "gpt-5-codex",
	} {
		if isCodexResponsesLiteModel(model) {
			t.Errorf("%s should keep the plain shape", model)
		}
	}
}

func TestCodexCatalogueKeepsGPT56AndAddsGPT62(t *testing.T) {
	have := map[string]bool{}
	for _, model := range Models() {
		if model.Provider == "openai-codex" {
			have[model.WireID()] = true
		}
	}
	for _, id := range []string{
		"gpt-6.2-sol", "gpt-6.2-astra", "gpt-6.1-terra", "gpt-6.1-sol",
		"gpt-6-astra", "gpt-6-sol", "gpt-6-luna",
		"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
		"gpt-5.5", "gpt-daybreak-blue-latest", "gpt-daybreak-red-latest",
		"gpt-reserve", "codex-auto-review",
	} {
		if !have[id] {
			t.Errorf("the Codex catalogue lacks %s", id)
		}
	}
}
