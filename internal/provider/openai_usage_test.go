package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOpenAIStreamCountsRepeatedUsageOnce is the billing guard for
// OpenAI-compatible servers that repeat the request-wide usage on every chunk.
//
// StepFun's plan endpoint and several other compatible servers send the same
// cumulative counters on each chunk instead of only on the final one. The agent
// sums every usage event it is handed, so forwarding each chunk as-is charged
// the same tokens once per chunk: a real session recorded tens of millions of
// prompt tokens for a handful of steps and the cost ran far ahead of the
// provider's own dashboard. The client must emit only the increase.
func TestOpenAIStreamCountsRepeatedUsageOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"content":"Hel"}}],"usage":{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101}}`,
			`{"choices":[{"delta":{"content":"lo"}}],"usage":{"prompt_tokens":100,"completion_tokens":2,"total_tokens":102}}`,
			`{"choices":[{"delta":{"content":"!"}}],"usage":{"prompt_tokens":100,"completion_tokens":3,"total_tokens":103}}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":3,"total_tokens":103}}`,
			`[DONE]`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "stepfun-plan", Label: "StepFun Plan", Kind: KindOpenAI}, server.URL, "test-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	events := collect(t, client, ChatRequest{
		Model:    "step-3.7-flash",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})

	total := 0
	for _, event := range events {
		if event.Type == EventUsage && event.Usage != nil {
			total += event.Usage.TotalTokens
		}
	}
	if total != 103 {
		t.Errorf("usage total = %d, want the final cumulative value 103 counted once", total)
	}
}

func TestOpenAIStreamTracksCachedTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"content":"Hi"}}],"usage":{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101,"prompt_tokens_details":{"cached_tokens":80}}}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`[DONE]`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "openai", Label: "OpenAI", Kind: KindOpenAI}, server.URL, "test-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	events := collect(t, client, ChatRequest{
		Model:    "gpt-5.4-mini",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})

	var gotUsage *Usage
	for _, event := range events {
		if event.Type == EventUsage && event.Usage != nil {
			gotUsage = event.Usage
		}
	}
	if gotUsage == nil {
		t.Fatalf("expected usage event")
	}
	if gotUsage.CacheReadTokens != 80 {
		t.Errorf("CacheReadTokens = %d, want 80", gotUsage.CacheReadTokens)
	}
}

// TestOpenAIStreamTracksDeepSeekCacheTokens guards DeepSeek's disk cache
// accounting. DeepSeek reports cache hits through prompt_cache_hit_tokens and
// misses through prompt_cache_miss_tokens rather than the OpenAI
// prompt_tokens_details.cached_tokens field. A hit is priced at a tenth of the
// full input rate, so swallowing it billed a cached prefix at the uncached
// price: the same cheap DeepSeek context cost as much as a cold one.
func TestOpenAIStreamTracksDeepSeekCacheTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"content":"Hi"}}],"usage":{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101,"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20}}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`[DONE]`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "deepseek", Label: "DeepSeek", Kind: KindOpenAI}, server.URL, "test-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	events := collect(t, client, ChatRequest{
		Model:    "deepseek-v4-pro",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})

	var gotUsage *Usage
	for _, event := range events {
		if event.Type == EventUsage && event.Usage != nil {
			gotUsage = event.Usage
		}
	}
	if gotUsage == nil {
		t.Fatalf("expected usage event")
	}
	if gotUsage.CacheReadTokens != 80 {
		t.Errorf("CacheReadTokens = %d, want 80", gotUsage.CacheReadTokens)
	}
	// The miss count is already inside prompt_tokens, so it must not show up as
	// additional cache. A prompt that is 80% cached must price 20 tokens at the
	// full rate and 80 at a tenth: 20 + 80*0.10 = 28 dollars per million, not
	// the cold 100. Only the hit influences the cached part.
	pricing := Pricing{InputPerMillion: 1.00, OutputPerMillion: 1.00}
	cost := pricing.Cost(Usage{PromptTokens: 100, CacheReadTokens: 80})
	if diff := cost - 0.000028; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost = %v, want 0.000028 (20 full + 80 cached at a tenth)", cost)
	}
}
