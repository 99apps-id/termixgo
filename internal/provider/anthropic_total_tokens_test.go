package provider

import (
	"context"
	"testing"
)

// TestAnthropicUsageCarriesTotalTokens pins the audit fix: Anthropic reports
// the prompt side on message_start and the output side on message_delta and
// never sends a total, so the client must join the two halves. Before the
// fix the summed usage had TotalTokens 0, which blanked the status-bar
// token count, the /cost total and the rolling quota window.
func TestAnthropicUsageCarriesTotalTokens(t *testing.T) {
	server := newTestServer(t, streamFrames(
		`{"type":"message_start","message":{"usage":{"input_tokens":40,"cache_read_input_tokens":60,"cache_creation_input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	))

	client, err := newHTTPClient(Provider{ID: "anthropic", Label: "Anthropic", Kind: KindAnthropic}, server.URL, "test-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}

	var total Usage
	if err := client.Stream(context.Background(), ChatRequest{Model: "claude-sonnet-5"}, func(event StreamEvent) error {
		if event.Type == EventUsage && event.Usage != nil {
			total = total.Add(*event.Usage)
		}
		return nil
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	want := Usage{PromptTokens: 110, CompletionTokens: 7, TotalTokens: 117, CacheReadTokens: 60, CacheWriteTokens: 10}
	if total != want {
		t.Errorf("usage = %+v, want %+v", total, want)
	}
}

// TestGoogleUsageCarriesThoughtsTokens pins the matching gap on the Google
// client: usageMetadata reports thoughtsTokenCount, and dropping it made
// reasoning spend invisible on Gemini models.
func TestGoogleUsageCarriesThoughtsTokens(t *testing.T) {
	url := googleUsageServer(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"Answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":6,"thoughtsTokenCount":4,"totalTokenCount":20,"cachedContentTokenCount":3}}`,
	})
	client, _ := newHTTPClient(Provider{ID: "google", Label: "Google", Kind: KindGoogle}, url, "google-key")
	events := collect(t, client, ChatRequest{Model: "gemini-3-pro", Messages: []Message{{Role: RoleUser, Content: "hi"}}})

	want := Usage{PromptTokens: 10, CompletionTokens: 6, TotalTokens: 20, CacheReadTokens: 3, ReasoningTokens: 4}
	if got := sumUsageEvents(events); got != want {
		t.Errorf("summed usage = %+v, want %+v", got, want)
	}
}
