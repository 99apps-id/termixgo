package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/provider"
)

// TestFormatUsageSegments pins the /cost per-model split: every segment gets
// a line naming its provider, model and billing mode, plan segments show no
// dollars, and an unpriced segment says so instead of printing $0.0000.
func TestFormatUsageSegments(t *testing.T) {
	segments := []agent.UsageSegment{
		{Provider: "openai", Model: "gpt-x", BillingMode: "api_key",
			Usage: provider.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110}, CostUSD: 0.5, Records: 3},
		{Provider: "claude-oauth", Model: "claude-y", BillingMode: "oauth_plan",
			Usage: provider.Usage{PromptTokens: 40, CompletionTokens: 5, TotalTokens: 45}, Records: 1, Unpriced: 1},
		{Provider: "ollama", Model: "local-z", BillingMode: "local",
			Usage: provider.Usage{TotalTokens: 9}, Records: 1},
		{Provider: "openrouter", Model: "auto", BillingMode: "unknown",
			Usage: provider.Usage{TotalTokens: 7}, Records: 1, Unpriced: 1},
	}
	out := formatUsageSegments(segments)
	for _, want := range []string{
		"openai/gpt-x (api_key): 100 in, 10 out, 110 total, about $0.5000",
		"claude-oauth/claude-y (oauth_plan): 40 in, 5 out, 45 total",
		"ollama/local-z (local): 0 in, 0 out, 9 total, free (local)",
		"openrouter/auto (unknown): 0 in, 0 out, 7 total, cost unknown",
		"BYOK (API Key): 110 tokens, spend about $0.5000",
		"OAuth (Plan): 45 tokens",
		"Local (Free): 9 tokens",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("segments output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "oauth_plan): 40 in, 5 out, 45 total,") {
		t.Errorf("a plan segment must not carry a dollar figure:\n%s", out)
	}
	if got := formatUsageSegments(nil); got != "" {
		t.Errorf("no segments should render nothing, got %q", got)
	}
}
