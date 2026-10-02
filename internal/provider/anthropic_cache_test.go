package provider

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicPromptCachingPayload(t *testing.T) {
	recorder, server := newPayloadRecorder(t)
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "anthropic", Label: "Anthropic", Kind: KindAnthropic}, server.URL, "anthropic-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}

	collect(t, client, ChatRequest{
		Model:       "claude-sonnet-4-5",
		SystemParts: []string{"static base instructions", "dynamic current plan"},
		Messages: []Message{
			{Role: RoleUser, Content: "first turn"},
			{Role: RoleAssistant, Content: "first answer"},
			{Role: RoleUser, Content: "second turn"},
		},
		Tools: []ToolDef{
			{Name: "tool1", Description: "first tool"},
			{Name: "tool2", Description: "second tool"},
		},
	})

	body := recorder.payload(t)

	// Check anthropic-beta header
	if beta := recorder.headers.Get("anthropic-beta"); beta != "prompt-caching-2024-07-31" {
		t.Errorf("anthropic-beta header = %q, want prompt-caching-2024-07-31", beta)
	}

	// Check system blocks
	systemRaw, ok := body["system"].([]any)
	if !ok || len(systemRaw) != 2 {
		t.Fatalf("body[system] should be 2 blocks, got %#v", body["system"])
	}
	sysBlock0 := object(t, systemRaw[0], "system", "0")
	if sysBlock0["text"] != "static base instructions" {
		t.Errorf("sysBlock0 text = %v", sysBlock0["text"])
	}
	cacheCtrl0 := object(t, sysBlock0["cache_control"], "cache_control")
	if cacheCtrl0["type"] != "ephemeral" {
		t.Errorf("sysBlock0 cache_control type = %v", cacheCtrl0["type"])
	}

	// Check tool cache_control on the last tool
	toolsRaw := array(t, body, "tools")
	if len(toolsRaw) != 2 {
		t.Fatalf("tools length = %d, want 2", len(toolsRaw))
	}
	tool1 := object(t, toolsRaw[1], "tools", "1")
	cacheCtrlTool := object(t, tool1["cache_control"], "cache_control")
	if cacheCtrlTool["type"] != "ephemeral" {
		t.Errorf("tool1 cache_control type = %v", cacheCtrlTool["type"])
	}

	// Check message cache_control on the second-to-last turn
	messagesRaw := array(t, body, "messages")
	if len(messagesRaw) != 3 {
		t.Fatalf("messages length = %d, want 3", len(messagesRaw))
	}
	msg1 := object(t, messagesRaw[1], "messages", "1")
	contentRaw, ok := msg1["content"].([]any)
	if !ok || len(contentRaw) == 0 {
		t.Fatalf("expected message content blocks in turn 1, got %#v", msg1["content"])
	}
	contentBlock := object(t, contentRaw[len(contentRaw)-1], "content", "last")
	cacheCtrlMsg := object(t, contentBlock["cache_control"], "cache_control")
	if cacheCtrlMsg["type"] != "ephemeral" {
		t.Errorf("message cache_control type = %v", cacheCtrlMsg["type"])
	}
}

func TestAnthropicUsageTracksCachedTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","usage":{"input_tokens":500,"cache_creation_input_tokens":100,"cache_read_input_tokens":400}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","usage":{"output_tokens":10}}`,
			`{"type":"message_stop"}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", "message", chunk)
		}
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "anthropic", Label: "Anthropic", Kind: KindAnthropic}, server.URL, "test-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}

	events := collect(t, client, ChatRequest{
		Model:    "claude-sonnet-4-5",
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})

	var gotUsage []Usage
	for _, event := range events {
		if event.Type == EventUsage && event.Usage != nil {
			gotUsage = append(gotUsage, *event.Usage)
		}
	}

	if len(gotUsage) < 2 {
		t.Fatalf("expected at least 2 usage events, got %d", len(gotUsage))
	}

	// First usage from message_start
	u0 := gotUsage[0]
	if u0.PromptTokens != 900 { // 500 miss + 400 hit = 900 total prompt tokens
		t.Errorf("PromptTokens = %d, want 900", u0.PromptTokens)
	}
	if u0.CacheReadTokens != 400 {
		t.Errorf("CacheReadTokens = %d, want 400", u0.CacheReadTokens)
	}
	if u0.CacheWriteTokens != 100 {
		t.Errorf("CacheWriteTokens = %d, want 100", u0.CacheWriteTokens)
	}

	// Second usage from message_delta
	u1 := gotUsage[1]
	if u1.CompletionTokens != 10 {
		t.Errorf("CompletionTokens = %d, want 10", u1.CompletionTokens)
	}
}

func TestPricingCostCachedTokensDiscount(t *testing.T) {
	// Rate: $10/M input, $50/M output
	pricing := Pricing{InputPerMillion: 10.0, OutputPerMillion: 50.0}

	// Standard un-cached tokens
	costStandard := pricing.Cost(Usage{
		PromptTokens:     1_000_000,
		CompletionTokens: 0,
		CacheReadTokens:  0,
	})
	if math.Abs(costStandard-10.0) > 1e-9 {
		t.Errorf("costStandard = %v, want 10.0", costStandard)
	}

	// 100% cached tokens: 90% discount -> $1.0 instead of $10.0
	costCached := pricing.Cost(Usage{
		PromptTokens:     1_000_000,
		CompletionTokens: 0,
		CacheReadTokens:  1_000_000,
	})
	if math.Abs(costCached-1.0) > 1e-9 {
		t.Errorf("costCached = %v, want 1.0", costCached)
	}

	// 50% cached: 500k regular ($5) + 500k cached ($0.5) = $5.5
	costMixed := pricing.Cost(Usage{
		PromptTokens:     1_000_000,
		CompletionTokens: 0,
		CacheReadTokens:  500_000,
	})
	if math.Abs(costMixed-5.5) > 1e-9 {
		t.Errorf("costMixed = %v, want 5.5", costMixed)
	}
}
