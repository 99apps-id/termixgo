package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestOpenAISendsAPromptCacheKeyOnlyToOpenAI(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{{"openai", true}, {"deepseek", false}, {"openai-compatible", false}} {
		recorder, server := newPayloadRecorder(t)
		client, err := newHTTPClient(Provider{ID: tc.id, Label: tc.id, Kind: KindOpenAI}, server.URL, "k")
		if err != nil {
			t.Fatal(err)
		}
		collect(t, client, ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
		key, has := recorder.payload(t)["prompt_cache_key"].(string)
		server.Close()
		if has != tc.want {
			t.Errorf("%s: prompt_cache_key present = %v, want %v", tc.id, has, tc.want)
		}
		if has && !strings.HasPrefix(key, "termixgo-") {
			t.Errorf("%s: key = %q", tc.id, key)
		}
	}
}

func TestPromptCacheKeyIsStablePerSession(t *testing.T) {
	first := promptCacheKey("a")
	second := promptCacheKey("a")
	other := promptCacheKey("b")
	if first != second || first == other {
		t.Fatalf("the key must be stable for one session and differ between sessions")
	}
}

// A backend that refuses the field is asked again without it.
func TestCodexRetriesWithoutAPromptCacheKeyItRejects(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		count := len(bodies)
		mu.Unlock()
		if count == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported parameter: prompt_cache_key"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
	}))
	defer server.Close()
	client, err := newHTTPClient(Provider{ID: "openai-codex", Label: "Codex", Kind: KindOpenAI, OAuth: true}, server.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.(*codexClient); !ok {
		t.Fatalf("openai-codex should build the Codex client, got %T", client)
	}
	if err := client.Stream(t.Context(), ChatRequest{Model: "gpt-5.2", Messages: []Message{{Role: RoleUser, Content: "hi"}}}, func(StreamEvent) error { return nil }); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want the rejected one and the retry", len(bodies))
	}
	if _, has := bodies[0]["prompt_cache_key"]; !has {
		t.Fatalf("the first request should carry the key")
	}
	if _, has := bodies[1]["prompt_cache_key"]; has {
		t.Fatalf("the retry must drop the rejected field")
	}
}
