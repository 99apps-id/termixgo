package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/app"
	"github.com/99apps-id/termixgo/internal/provider"
)

func TestSlashQuotaCommand(t *testing.T) {
	model := chatModel(t)
	tracker := model.app.QuotaTracker()
	if tracker == nil {
		t.Fatalf("quota tracker should be initialized on app")
	}

	// Record sample 5-hour usage
	tracker.RecordUsage("antigravity", "gemini-2.5-pro", provider.Usage{
		PromptTokens:     1500,
		CompletionTokens: 500,
		TotalTokens:      2000,
	})
	tracker.RecordRateLimit("anthropic", &provider.RateLimitInfo{
		TokensLimit:     100000,
		TokensRemaining: 85000,
		RequestsLimit:   1000,
		RequestsRemaining: 950,
	})

	// Run /quota command
	after, _ := model.submit("/quota")
	view := after.View()
	if !strings.Contains(view, "5-Hour Rolling Quota") {
		t.Fatalf("expected quota header in view, got:\n%s", view)
	}
	if !strings.Contains(view, "5-Hour Window Activity") {
		t.Fatalf("expected 5-hour window activity section in view, got:\n%s", view)
	}
	if !strings.Contains(view, "not fetched yet") {
		t.Fatalf("expected not-fetched note without a snapshot, got:\n%s", view)
	}

	// An official snapshot must be shown first, ahead of local counts.
	fetchedAt := time.Now().Add(-2 * time.Minute)
	tracker.RecordSnapshot(prov(model), &provider.QuotaSnapshot{
		Provider:  prov(model),
		FetchedAt: fetchedAt,
		Windows: []provider.QuotaWindow{
			{Key: provider.QuotaSessionKey, DisplayName: "Session (5h)", UsedPercent: 22},
		},
	})
	after, _ = model.submit("/quota")
	view = after.View()
	if !strings.Contains(view, "Official Provider Usage") {
		t.Fatalf("expected official provider section in view, got:\n%s", view)
	}
	if !strings.Contains(view, "22.0% used") {
		t.Fatalf("expected official used percent in view, got:\n%s", view)
	}
}

func prov(m *Model) string { return m.app.CurrentModel().Provider }

func TestQuotaGaugeFormatting(t *testing.T) {
	gauge := app.VisualGauge(75.0, 10)
	if !strings.Contains(gauge, "75%") {
		t.Fatalf("expected 75%% in gauge, got %q", gauge)
	}
	if !strings.Contains(gauge, "█") {
		t.Fatalf("expected filled block in gauge, got %q", gauge)
	}

	gaugeZero := app.VisualGauge(0.0, 10)
	if !strings.Contains(gaugeZero, "0%") {
		t.Fatalf("expected 0%% in gauge, got %q", gaugeZero)
	}

	gaugeFull := app.VisualGauge(100.0, 10)
	if !strings.Contains(gaugeFull, "100%") {
		t.Fatalf("expected 100%% in gauge, got %q", gaugeFull)
	}
}

func TestHeaderDisplaysQuotaGauge(t *testing.T) {
	model := chatModel(t)
	model.width = 300
	tracker := model.app.QuotaTracker()

	prov := model.app.CurrentModel().Provider
	tracker.RecordUsage(prov, model.app.CurrentModel().ID, provider.Usage{
		PromptTokens: 10000,
		CompletionTokens: 2000,
		TotalTokens: 12000,
	})

	view := model.viewHeader()
	if !strings.Contains(view, "5h:") {
		t.Fatalf("header should display 5h quota when space permits, got:\n%s", view)
	}
}
