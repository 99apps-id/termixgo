package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestServer serves one handler for the length of a test.
func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// TestAnthropicUsageIsCountedOnceAcrossMessages keeps the token accounting
// honest on a multi-message response.
//
// The counters Anthropic streams are cumulative for the request, and a response
// can carry more than one message: interleaved thinking is in the OAuth beta
// header list, and each round reports the running total again. The agent sums
// every usage event it is handed, so forwarding each report raw charged the same
// prompt tokens once per message. The OpenAI reader already dedups this way;
// this is the same guard, and the same figure the status line and cost budget
// read.
func TestAnthropicUsageIsCountedOnceAcrossMessages(t *testing.T) {
	server := newTestServer(t, streamFrames(
		`{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
		// A second message in the same response repeats the prompt count.
		`{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":5}}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"second"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","usage":{"output_tokens":9}}`,
		`{"type":"message_stop"}`,
	))

	client, err := newHTTPClient(Provider{ID: "anthropic", Label: "Anthropic", Kind: KindAnthropic}, server.URL, "test-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}

	var total Usage
	text := ""
	if err := client.Stream(context.Background(), ChatRequest{Model: "claude-sonnet-5"}, func(event StreamEvent) error {
		switch event.Type {
		case EventUsage:
			if event.Usage != nil {
				total = total.Add(*event.Usage)
			}
		case EventTextDelta:
			text += event.Text
		}
		return nil
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if text != "firstsecond" {
		t.Errorf("text = %q, want both messages to stream through", text)
	}
	if total.PromptTokens != 100 {
		t.Errorf("prompt tokens = %d, want 100: the request sent 100, twice reported is still 100", total.PromptTokens)
	}
	if total.CompletionTokens != 9 {
		t.Errorf("completion tokens = %d, want the final cumulative figure", total.CompletionTokens)
	}
}

// TestAnthropicCacheTokensAreNotReCounted covers the cached prefix, which the
// prompt total already includes: a repeat report must not bill it again.
func TestAnthropicCacheTokensAreNotReCounted(t *testing.T) {
	server := newTestServer(t, streamFrames(
		`{"type":"message_start","message":{"usage":{"input_tokens":40,"cache_read_input_tokens":60,"cache_creation_input_tokens":0}}}`,
		`{"type":"message_delta","usage":{"output_tokens":7}}`,
		`{"type":"message_start","message":{"usage":{"input_tokens":40,"cache_read_input_tokens":60,"cache_creation_input_tokens":0}}}`,
		`{"type":"message_delta","usage":{"output_tokens":7}}`,
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
	if total.PromptTokens != 100 || total.CacheReadTokens != 60 {
		t.Errorf("usage = %+v, want the cache counters taken once", total)
	}
	if total.CompletionTokens != 7 {
		t.Errorf("completion tokens = %d, want 7", total.CompletionTokens)
	}
}
