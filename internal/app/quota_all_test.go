package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/oauth"
	"github.com/99apps-id/termixgo/internal/provider"
)

// quotaStub answers only the usage call; the rest of the interface is never
// reached by the refresh.
type quotaStub struct {
	provider.Client
	snap  *provider.QuotaSnapshot
	calls *int32
}

func (s quotaStub) FetchQuota(context.Context) *provider.QuotaSnapshot {
	atomic.AddInt32(s.calls, 1)
	return s.snap
}

func loginClaude(t *testing.T, application *App) {
	t.Helper()
	if err := provider.OAuthStore(application.Secrets()).Save("claude-oauth", oauth.Token{Access: "demo"}); err != nil {
		t.Fatalf("save login: %v", err)
	}
	application.forgetQuotaRows()
}

func claudeSnapshot(usedSession, usedWeekly float64) *provider.QuotaSnapshot {
	reset := time.Now().Add(2*time.Hour + 14*time.Minute)
	return &provider.QuotaSnapshot{
		Provider:  "claude-oauth",
		FetchedAt: time.Now(),
		Windows: []provider.QuotaWindow{
			{Key: provider.QuotaSessionKey, DisplayName: "Session (5h)", UsedPercent: usedSession, ResetsAt: &reset},
			{Key: provider.QuotaWeeklyKey, DisplayName: "Weekly", UsedPercent: usedWeekly},
		},
	}
}

// Only providers with a login are covered, and the row says what is left, not
// what was used.
func TestQuotaRowsReportWhatIsLeftForLoggedInProviders(t *testing.T) {
	application := newTestApp(t)
	if rows := application.QuotaRows(); len(rows) != 0 {
		t.Fatalf("no login, no row: %+v", rows)
	}
	loginClaude(t, application)
	application.QuotaTracker().RecordSnapshot("claude-oauth", claudeSnapshot(28, 59))
	application.forgetQuotaRows()

	rows := application.QuotaRows()
	if len(rows) != 1 || rows[0].Provider != "claude-oauth" || rows[0].Label != "Claude" || rows[0].State != QuotaKnown {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Session == nil || rows[0].Session.LeftPercent != 72 || rows[0].Session.ResetsIn < 2*time.Hour {
		t.Fatalf("session = %+v", rows[0].Session)
	}
	if rows[0].Weekly == nil || rows[0].Weekly.LeftPercent != 41 {
		t.Fatalf("weekly = %+v", rows[0].Weekly)
	}
}

func TestQuotaRowIsPendingUntilTheFirstFetchLands(t *testing.T) {
	application := newTestApp(t)
	loginClaude(t, application)
	rows := application.QuotaRows()
	if len(rows) != 1 || rows[0].State != QuotaPending {
		t.Fatalf("rows = %+v", rows)
	}
}

// A provider fetched a moment ago is not asked again until forced, a failure
// keeps the last good figures, and a provider with no endpoint reads n/a.
func TestRefreshAllQuotaFetchesThrottlesAndKeepsLastGood(t *testing.T) {
	application := newTestApp(t)
	loginClaude(t, application)
	var calls int32
	current := claudeSnapshot(10, 20)
	application.quotaClientFor = func(provider.Provider) (provider.Client, error) {
		return quotaStub{snap: current, calls: &calls}, nil
	}

	application.RefreshAllQuota(context.Background(), false)
	if rows := application.QuotaRows(); len(rows) != 1 || rows[0].State != QuotaKnown || rows[0].Session.LeftPercent != 90 {
		t.Fatalf("first fetch: %+v", rows)
	}
	application.RefreshAllQuota(context.Background(), false)
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("a fresh snapshot must not be fetched again, calls = %d", calls)
	}
	application.RefreshAllQuota(context.Background(), true)
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("force must fetch, calls = %d", calls)
	}

	current = &provider.QuotaSnapshot{Provider: "claude-oauth", FetchedAt: time.Now(), Unavailable: "network down"}
	application.RefreshAllQuota(context.Background(), true)
	if rows := application.QuotaRows(); rows[0].State != QuotaKnown || rows[0].Session.LeftPercent != 90 {
		t.Fatalf("a failed fetch must keep the last good figures: %+v", rows[0])
	}
}

func TestQuotaRowReadsNotAvailableForAProviderWithoutAnEndpoint(t *testing.T) {
	application := newTestApp(t)
	loginClaude(t, application)
	var calls int32
	application.quotaClientFor = func(provider.Provider) (provider.Client, error) {
		return quotaStub{snap: &provider.QuotaSnapshot{Provider: "claude-oauth", FetchedAt: time.Now(), Unavailable: "no usage endpoint"}, calls: &calls}, nil
	}
	application.RefreshAllQuota(context.Background(), false)
	if rows := application.QuotaRows(); rows[0].State != QuotaUnavailable {
		t.Fatalf("rows = %+v", rows)
	}
}

// A provider that reports neither the 5-hour nor the weekly window still says
// something true: its tightest window.
func TestLeftWindowsFallBackToTheTightestWindow(t *testing.T) {
	snap := &provider.QuotaSnapshot{Windows: []provider.QuotaWindow{
		{Key: "daily_gemini", DisplayName: "Gemini daily", UsedPercent: 30},
		{Key: "daily_claude", DisplayName: "Claude daily", UsedPercent: 85},
	}}
	session, weekly, other, name := leftWindows(snap)
	if session != nil || weekly != nil || other == nil || other.LeftPercent != 15 || name != "Claude daily" {
		t.Fatalf("session=%v weekly=%v other=%+v name=%q", session, weekly, other, name)
	}
}

// A window that has rolled over since the figures were taken is not what is
// left any more; after a failed refresh the row must not show it as current.
func TestRolledOverWindowsAreNotShown(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(3 * time.Hour)
	snap := &provider.QuotaSnapshot{Windows: []provider.QuotaWindow{
		{Key: provider.QuotaSessionKey, UsedPercent: 95, ResetsAt: &past},
		{Key: provider.QuotaWeeklyKey, UsedPercent: 40, ResetsAt: &future},
	}}
	session, weekly, _, _ := leftWindows(snap)
	if session != nil {
		t.Fatalf("the 5h window reset a minute ago, so 5%% left is stale: %+v", session)
	}
	if weekly == nil || weekly.LeftPercent != 60 {
		t.Fatalf("weekly = %+v", weekly)
	}

	application := newTestApp(t)
	loginClaude(t, application)
	application.QuotaTracker().RecordSnapshot("claude-oauth", &provider.QuotaSnapshot{
		Provider: "claude-oauth", FetchedAt: time.Now(),
		Windows: []provider.QuotaWindow{{Key: provider.QuotaSessionKey, UsedPercent: 95, ResetsAt: &past}},
	})
	if rows := application.QuotaRows(); len(rows) != 1 || rows[0].State != QuotaPending {
		t.Fatalf("a row whose every window rolled over is pending again: %+v", rows)
	}
}

func TestOldSnapshotsAreMarkedStale(t *testing.T) {
	application := newTestApp(t)
	loginClaude(t, application)
	snap := claudeSnapshot(10, 20)
	snap.FetchedAt = time.Now().Add(-2 * time.Hour)
	application.QuotaTracker().RecordSnapshot("claude-oauth", snap)
	if rows := application.QuotaRows(); len(rows) != 1 || !rows[0].Stale {
		t.Fatalf("rows = %+v", rows)
	}
}
