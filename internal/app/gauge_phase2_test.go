package app

import (
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/provider"
)

// TestQuotaGaugeShowsSnapshotAge checks the G-05 marker: the header gauge
// names how old its snapshot is, the way /quota already did, so a cached
// figure is not read as live.
func TestQuotaGaugeShowsSnapshotAge(t *testing.T) {
	tracker := NewQuotaTracker()
	snap := snapshotWithSession(22, 0)
	snap.FetchedAt = time.Now().Add(-5 * time.Minute)
	tracker.RecordSnapshot("codex", snap)

	gauge := tracker.ActiveQuotaGauge("codex", 8)
	if !containsStr(gauge, "fetched 5m") || !containsStr(gauge, "ago") {
		t.Errorf("gauge %q should carry the snapshot age", gauge)
	}
	if got := SnapshotAgeText(time.Time{}); got != "" {
		t.Errorf("a snapshot without a fetch time should have no age text, got %q", got)
	}
}

// TestQuotaGaugeLocalFallbackIsLabelledNotBarred checks the G-04 fallback:
// with no official snapshot, the local 5-hour counts appear without a bar
// and labelled as a local estimate, never dressed as an official gauge.
func TestQuotaGaugeLocalFallbackIsLabelledNotBarred(t *testing.T) {
	tracker := NewQuotaTracker()
	tracker.RecordUsage("codex", "gpt-5.2", provider.Usage{TotalTokens: 42000})

	gauge := tracker.ActiveQuotaGauge("codex", 8)
	if !containsStr(gauge, "[5h: 1r/42k]") {
		t.Errorf("fallback should show the local counts, got %q", gauge)
	}
	if !containsStr(gauge, LocalEstimateLabel) {
		t.Errorf("fallback %q should be labelled %q", gauge, LocalEstimateLabel)
	}
	if strings.Contains(gauge, "█") || strings.Contains(gauge, "░") {
		t.Errorf("fallback %q should not draw a bar: it has no official denominator", gauge)
	}
}

// TestQuotaRefNamesTheKnownWindow covers the quota_ref wiring at the
// tracker level: the session window wins, a plain weekly window is next, a
// model-scoped window is named only for its own model, and an unknown
// provider gets nothing rather than a guessed window.
func TestQuotaRefNamesTheKnownWindow(t *testing.T) {
	tracker := NewQuotaTracker()
	if got := tracker.QuotaRef("codex", "gpt-5.2"); got != "" {
		t.Errorf("no snapshot should mean no ref, got %q", got)
	}
	tracker.RecordSnapshot("codex", &provider.QuotaSnapshot{
		Provider: "codex",
		Windows: []provider.QuotaWindow{
			{Key: provider.QuotaWeeklyKey, UsedPercent: 10},
			{Key: provider.QuotaSessionKey, UsedPercent: 22},
		},
	})
	if got := tracker.QuotaRef("codex", "gpt-5.2"); got != "codex:session" {
		t.Errorf("session window should win, got %q", got)
	}

	weeklyOnly := NewQuotaTracker()
	weeklyOnly.RecordSnapshot("codex", &provider.QuotaSnapshot{
		Provider: "codex",
		Windows:  []provider.QuotaWindow{{Key: provider.QuotaWeeklyKey, UsedPercent: 10}},
	})
	if got := weeklyOnly.QuotaRef("codex", "gpt-5.2"); got != "codex:weekly" {
		t.Errorf("weekly-only snapshot should name the weekly window, got %q", got)
	}

	scoped := NewQuotaTracker()
	scoped.RecordSnapshot("anthropic", &provider.QuotaSnapshot{
		Provider: "anthropic",
		Windows:  []provider.QuotaWindow{{Key: "weekly_sonnet", UsedPercent: 40}},
	})
	if got := scoped.QuotaRef("anthropic", "claude-sonnet-5"); got != "anthropic:weekly_sonnet" {
		t.Errorf("a sonnet record should name the sonnet window, got %q", got)
	}
	if got := scoped.QuotaRef("anthropic", "claude-opus-4-1"); got != "" {
		t.Errorf("an opus record must not be stamped with the sonnet window, got %q", got)
	}
}

// TestRecordStampsQuotaRefFromSnapshot drives the emit path with an
// official snapshot stored for the active provider: the ledger record must
// carry the window's identity. A second app without any snapshot must
// leave the same record's quota_ref null.
func TestRecordStampsQuotaRefFromSnapshot(t *testing.T) {
	usage := provider.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}

	withSnap, server := readyApp(t)
	defer server.Close()
	defer withSnap.Shutdown()
	withSnap.QuotaTracker().RecordSnapshot("ollama", &provider.QuotaSnapshot{
		Provider:  "ollama",
		FetchedAt: time.Now(),
		Windows:   []provider.QuotaWindow{{Key: provider.QuotaSessionKey, UsedPercent: 10}},
	})
	withSnap.Emit(agent.Event{Kind: agent.EventTurnStart})
	withSnap.Emit(agent.Event{Kind: agent.EventUsage, Usage: usage})
	withSnap.Emit(agent.Event{Kind: agent.EventTurnEnd, StopReason: "stop", Usage: usage})

	records := readLedger(t, withSnap)
	found := false
	for _, record := range records {
		if record.Kind != agent.LedgerKindMain {
			continue
		}
		found = true
		if record.QuotaRef == nil || *record.QuotaRef != "ollama:session" {
			t.Errorf("main record quota_ref = %v, want ollama:session", record.QuotaRef)
		}
		if record.Credits != nil {
			t.Errorf("credits should stay null, got %v", *record.Credits)
		}
	}
	if !found {
		t.Fatalf("no main record in the ledger: %+v", records)
	}

	withoutSnap, server2 := readyApp(t)
	defer server2.Close()
	defer withoutSnap.Shutdown()
	withoutSnap.Emit(agent.Event{Kind: agent.EventTurnStart})
	withoutSnap.Emit(agent.Event{Kind: agent.EventUsage, Usage: usage})
	withoutSnap.Emit(agent.Event{Kind: agent.EventTurnEnd, StopReason: "stop", Usage: usage})

	for _, record := range readLedger(t, withoutSnap) {
		if record.Kind == agent.LedgerKindMain && record.QuotaRef != nil {
			t.Errorf("without a snapshot the quota_ref must stay null, got %q", *record.QuotaRef)
		}
	}
}

func readLedger(t *testing.T, application *App) []agent.UsageRecord {
	t.Helper()
	session := application.Session()
	path, err := agent.SessionLedgerPath(session.ID())
	if err != nil {
		t.Fatalf("SessionLedgerPath: %v", err)
	}
	records, err := agent.ReadUsageLedger(path)
	if err != nil {
		t.Fatalf("ReadUsageLedger: %v", err)
	}
	return records
}

// TestStatusLabelsCumulativeAsBilled checks the G-02 relabel in /status:
// the cumulative session total is named billed, apart from the context
// figure, which now reads against the model window.
func TestStatusLabelsCumulativeAsBilled(t *testing.T) {
	application := newTestApp(t)
	status := application.Status()
	if !strings.Contains(status, "billed: 0 in, 0 out") {
		t.Errorf("status should label the cumulative total as billed, got:\n%s", status)
	}
	if strings.Contains(status, "tokens: 0 in") {
		t.Errorf("status should not present the cumulative total as context tokens, got:\n%s", status)
	}
	if !strings.Contains(status, "context: ctx:") || !strings.Contains(status, "of window") || !strings.Contains(status, "compact:") {
		t.Errorf("status context should carry the window and compaction figures, got:\n%s", status)
	}
}

// TestContextUsageShowsBothFigures pins the /status and status-bar figure
// at the app level: both the window percentage and the separate compaction
// proximity are present.
func TestContextUsageShowsBothFigures(t *testing.T) {
	application := newTestApp(t)
	usage := application.ContextUsage()
	for _, want := range []string{"ctx:", "of window", "compact:"} {
		if !strings.Contains(usage, want) {
			t.Errorf("context usage %q should contain %q", usage, want)
		}
	}
}
