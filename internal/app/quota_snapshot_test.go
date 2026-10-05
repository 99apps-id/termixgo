package app

import (
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/provider"
)

func snapshotWithSession(used float64, resetIn time.Duration) *provider.QuotaSnapshot {
	var reset *time.Time
	if resetIn > 0 {
		t := time.Now().Add(resetIn).Truncate(time.Second)
		reset = &t
	}
	return &provider.QuotaSnapshot{
		Provider:  "codex",
		FetchedAt: time.Now(),
		Windows: []provider.QuotaWindow{
			{Key: provider.QuotaSessionKey, DisplayName: "Session (5h)", UsedPercent: used, ResetsAt: reset},
		},
	}
}

// The official snapshot must win over local estimates and live headers:
// the header gauge shows the provider's own numbers, including the reset
// countdown, instead of the local turn count.
func TestQuotaGaugePrefersOfficialSnapshot(t *testing.T) {
	tracker := NewQuotaTracker()
	tracker.RecordUsage("codex", "gpt-5.2", provider.Usage{TotalTokens: 42000})
	tracker.RecordSnapshot("codex", snapshotWithSession(22, 3*time.Hour+12*time.Minute))

	gauge := tracker.ActiveQuotaGauge("codex", 8)
	if gauge == "" {
		t.Fatal("expected a gauge from the official snapshot")
	}
	// Local fallback would read "[5h: 1r/42k]"; the official gauge is a
	// percent bar with a reset countdown, e.g. "[██████░░] 78%(3h12m0s)".
	if got := VisualGauge(78, 8); gauge == "[5h: 1r/42k]" || gauge == got {
		t.Errorf("gauge %q looks like the local estimate, want official snapshot with reset", gauge)
	}
	for _, want := range []string{"78%", "(3h12m0s)"} {
		if !containsStr(gauge, want) {
			t.Errorf("gauge %q should contain %q", gauge, want)
		}
	}
}

// Without a session window the gauge must fall back gracefully: live
// headers first, then the local activity estimate, never blank when data
// exists.
func TestQuotaGaugeSnapshotFallbackChain(t *testing.T) {
	tracker := NewQuotaTracker()
	tracker.RecordUsage("codex", "gpt-5.2", provider.Usage{TotalTokens: 42000})
	// Snapshot with only a weekly window: no 5h numbers to show.
	tracker.RecordSnapshot("codex", &provider.QuotaSnapshot{
		Provider: "codex",
		Windows:  []provider.QuotaWindow{{Key: provider.QuotaWeeklyKey, UsedPercent: 10}},
	})
	if got := tracker.ActiveQuotaGauge("codex", 8); got != "[5h: 1r/42k]" {
		t.Errorf("weekly-only snapshot should fall back to local estimate, got %q", got)
	}

	// Clearing the snapshot restores the plain local gauge.
	tracker.RecordSnapshot("codex", nil)
	if tracker.Snapshot("codex") != nil {
		t.Error("nil snapshot should clear the stored one")
	}
	if got := tracker.ActiveQuotaGauge("codex", 8); got != "[5h: 1r/42k]" {
		t.Errorf("cleared snapshot should show local estimate, got %q", got)
	}
}

// Providers that appear only via an official snapshot must show up in the
// tracked list so /quota renders them.
func TestQuotaTrackedProvidersIncludeSnapshots(t *testing.T) {
	tracker := NewQuotaTracker()
	tracker.RecordSnapshot("antigravity", &provider.QuotaSnapshot{
		Provider: "antigravity",
		Windows:  []provider.QuotaWindow{{Key: provider.QuotaSessionKey, UsedPercent: 5}},
	})
	found := false
	for _, p := range tracker.AllTrackedProviders() {
		if p == "antigravity" {
			found = true
		}
	}
	if !found {
		t.Error("snapshot-only provider should be listed as tracked")
	}
}

func containsStr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
