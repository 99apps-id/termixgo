package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/provider"
)

func TestStatusShowsTheCacheHitRate(t *testing.T) {
	model := chatModel(t)
	resize(model, 200, 40)
	if strings.Contains(stripANSI(model.viewStatus()), "cache") {
		t.Fatalf("no cache figure before any usage")
	}
	model.app.Session().AddUsage(provider.Usage{PromptTokens: 10000, CacheReadTokens: 8700, TotalTokens: 10100, CompletionTokens: 100})
	model.app.Emit(agent.Event{Kind: agent.EventUsage, Usage: provider.Usage{PromptTokens: 10000, CacheReadTokens: 8700, TotalTokens: 10100, CompletionTokens: 100}})
	if status := stripANSI(model.viewStatus()); !strings.Contains(status, "cache 87%") {
		t.Fatalf("status should show the hit rate:\n%s", status)
	}
}
