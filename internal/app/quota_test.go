package app

import (
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/provider"
)

func TestQuotaTrackerRollingWindow(t *testing.T) {
	tracker := NewQuotaTracker()

	tracker.RecordUsage("antigravity", "gemini-2.5-pro", provider.Usage{
		PromptTokens:     1000,
		CompletionTokens: 200,
		TotalTokens:      1200,
	})
	tracker.RecordUsage("antigravity", "gemini-2.5-pro", provider.Usage{
		PromptTokens:     2000,
		CompletionTokens: 300,
		TotalTokens:      2300,
	})

	stats := tracker.Stats("antigravity", 5*time.Hour)
	if stats.Requests != 2 {
		t.Errorf("expected 2 requests, got %d", stats.Requests)
	}
	if stats.TotalTokens != 3500 {
		t.Errorf("expected 3500 tokens, got %d", stats.TotalTokens)
	}
	if stats.OldestInWindow.IsZero() {
		t.Errorf("expected non-zero OldestInWindow")
	}
	if stats.ResetIn <= 0 || stats.ResetIn > 5*time.Hour {
		t.Errorf("expected resetIn in (0, 5h], got %v", stats.ResetIn)
	}

	// Another provider
	tracker.RecordUsage("claude", "claude-3-7-sonnet", provider.Usage{
		PromptTokens:     500,
		CompletionTokens: 100,
		TotalTokens:      600,
	})

	claudeStats := tracker.Stats("claude", 5*time.Hour)
	if claudeStats.Requests != 1 || claudeStats.TotalTokens != 600 {
		t.Errorf("expected 1 req / 600 tok for claude, got %d / %d", claudeStats.Requests, claudeStats.TotalTokens)
	}

	providers := tracker.AllTrackedProviders()
	if len(providers) != 2 {
		t.Errorf("expected 2 tracked providers, got %v", providers)
	}
}

func TestQuotaTrackerRateLimit(t *testing.T) {
	tracker := NewQuotaTracker()

	rl := &provider.RateLimitInfo{
		TokensLimit:     100000,
		TokensRemaining: 60000,
	}
	tracker.RecordRateLimit("claude", rl)

	stats := tracker.Stats("claude", 5*time.Hour)
	if stats.RateLimit == nil {
		t.Fatalf("expected rate limit info in stats")
	}
	if pct := stats.RateLimit.PercentTokensRemaining(); pct != 60.0 {
		t.Errorf("expected 60%% remaining, got %.1f%%", pct)
	}
}

func TestVisualGauge(t *testing.T) {
	gauge80 := VisualGauge(80, 10)
	if gauge80 != "[████████░░] 80%" {
		t.Errorf("expected '[████████░░] 80%%', got %q", gauge80)
	}

	gaugeUnknown := VisualGauge(-1, 10)
	if gaugeUnknown != "[?]" {
		t.Errorf("expected '[?]', got %q", gaugeUnknown)
	}
}
