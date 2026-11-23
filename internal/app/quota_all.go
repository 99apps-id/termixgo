package app

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/provider"
)

// QuotaLeft is what one usage window has left, as the provider reports it.
type QuotaLeft struct {
	// LeftPercent is 0-100 of the allowance still available.
	LeftPercent float64
	// ResetsIn is the time until the window rolls over; zero when unknown.
	ResetsIn time.Duration
}

// QuotaRowState says how much is known about one provider's usage.
type QuotaRowState int

const (
	// QuotaPending means no official figure has arrived yet.
	QuotaPending QuotaRowState = iota
	// QuotaKnown means the provider's own windows are on hand.
	QuotaKnown
	// QuotaUnavailable means the provider exposes no usage endpoint.
	QuotaUnavailable
)

// QuotaRow is one logged-in provider's remaining usage, the unit of the strip
// above the composer.
type QuotaRow struct {
	Provider string
	// Label is the short name shown in the strip: "Claude", "Codex".
	Label string
	// Session is the 5-hour window and Weekly the 7-day one. Other is the
	// tightest remaining window of a provider that reports neither.
	Session   *QuotaLeft
	Weekly    *QuotaLeft
	Other     *QuotaLeft
	OtherName string
	State     QuotaRowState
	FetchedAt time.Time
	// Stale means the figures are old because the refreshes have been failing.
	Stale bool
	// Active marks the provider of the current model.
	Active bool
}

// quotaLabels are the short names of the login providers.
var quotaLabels = map[string]string{
	"claude-oauth":   "Claude",
	"openai-codex":   "Codex",
	"antigravity":    "Antigravity",
	"agy-cli":        "agy CLI",
	"github-copilot": "Copilot",
	"muse":           "Muse",
	"xai-oauth":      "Grok",
}

func quotaLabel(info provider.Provider) string {
	if label, ok := quotaLabels[info.ID]; ok {
		return label
	}
	label := strings.TrimSpace(strings.TrimSuffix(info.Label, "(OAuth)"))
	if label == "" {
		return info.ID
	}
	return label
}

// quotaProviders lists what the strip covers: every provider that uses a login
// and has one stored, in the catalogue's order, plus the current provider when
// it is a prepaid plan that reports usage.
func (a *App) quotaProviders() []provider.Provider {
	a.mu.Lock()
	store := a.store
	current := a.model.Provider
	a.mu.Unlock()
	var out []provider.Provider
	for _, info := range provider.Providers() {
		switch {
		case info.OAuth && provider.KeySource(store, info.ID) != "":
			out = append(out, info)
		case info.ID == current && isOAuthOrPlanProvider(info.ID):
			out = append(out, info)
		}
	}
	return out
}

// QuotaRows reports every covered provider's remaining usage from the stored
// official snapshots. It reads memory only, so the view can call it on every
// paint; RefreshAllQuota is what keeps the snapshots current.
func (a *App) QuotaRows() []QuotaRow {
	tracker := a.QuotaTracker()
	a.mu.Lock()
	// The view asks on every paint, which is every 120 ms while an answer
	// streams; a second-old answer is as good and costs no lookups.
	version := tracker.SnapshotVersion()
	if a.quotaRowsMemo != nil && a.quotaRowsVersion == version && time.Since(a.quotaRowsAt) < time.Second {
		memo := append([]QuotaRow(nil), a.quotaRowsMemo...)
		a.mu.Unlock()
		return memo
	}
	current := a.model.Provider
	unavailable := make(map[string]bool, len(a.quotaUnavailable))
	for id := range a.quotaUnavailable {
		unavailable[id] = true
	}
	a.mu.Unlock()

	var rows []QuotaRow
	for _, info := range a.quotaProviders() {
		row := QuotaRow{Provider: info.ID, Label: quotaLabel(info), Active: info.ID == current}
		snap := tracker.Snapshot(info.ID)
		switch {
		case snap != nil && len(snap.Windows) > 0:
			row.State = QuotaKnown
			row.FetchedAt = snap.FetchedAt
			row.Stale = time.Since(snap.FetchedAt) > staleAfter
			row.Session, row.Weekly, row.Other, row.OtherName = leftWindows(snap)
			if row.Session == nil && row.Weekly == nil && row.Other == nil {
				// Every window has rolled over since the figures were taken.
				row.State = QuotaPending
			}
		case unavailable[info.ID]:
			row.State = QuotaUnavailable
		default:
			row.State = QuotaPending
		}
		rows = append(rows, row)
	}
	a.mu.Lock()
	a.quotaRowsMemo = append([]QuotaRow{}, rows...)
	a.quotaRowsAt = time.Now()
	a.quotaRowsVersion = version
	a.mu.Unlock()
	return rows
}

// forgetQuotaRows drops the memo so the next read sees fresh snapshots.
func (a *App) forgetQuotaRows() {
	a.mu.Lock()
	a.quotaRowsMemo = nil
	a.mu.Unlock()
}

// leftWindows turns a snapshot's windows into what is left. A window the
// provider names as the 5-hour or the weekly one fills its slot; a snapshot
// with neither keeps its tightest window so the row still says something true.
func leftWindows(snap *provider.QuotaSnapshot) (session, weekly, other *QuotaLeft, otherName string) {
	left := func(w provider.QuotaWindow) *QuotaLeft {
		// A window whose reset time has passed has rolled over since the figure
		// was taken: what it said is no longer what is left, and showing it
		// would be an old answer dressed as a current one.
		if w.ResetsAt != nil && !w.ResetsAt.After(time.Now()) {
			return nil
		}
		remaining := 100 - w.UsedPercent
		if remaining < 0 {
			remaining = 0
		}
		if remaining > 100 {
			remaining = 100
		}
		item := &QuotaLeft{LeftPercent: remaining}
		if w.ResetsAt != nil {
			if until := time.Until(*w.ResetsAt); until > 0 {
				item.ResetsIn = until
			}
		}
		return item
	}
	if w := snap.SessionWindow(); w != nil {
		session = left(*w)
	}
	for i := range snap.Windows {
		if snap.Windows[i].Key == provider.QuotaWeeklyKey {
			weekly = left(snap.Windows[i])
			break
		}
	}
	if session == nil && weekly == nil {
		windows := append([]provider.QuotaWindow(nil), snap.Windows...)
		sort.SliceStable(windows, func(i, j int) bool { return windows[i].UsedPercent > windows[j].UsedPercent })
		for _, w := range windows {
			if item := left(w); item != nil {
				other, otherName = item, w.DisplayName
				break
			}
		}
	}
	return session, weekly, other, otherName
}

// staleAfter is how old a snapshot may get before its row says so. Refreshes
// run every few minutes, so a snapshot this old means they have been failing.
const staleAfter = 30 * time.Minute

// quotaMinInterval is how soon a provider's usage is fetched again: the
// providers' endpoints are rate limited and the figures move slowly.
const quotaMinInterval = 3 * time.Minute

// quotaRefresh guards RefreshAllQuota against overlapping runs.
type quotaRefresh struct {
	mu      sync.Mutex
	running bool
}

func (q *quotaRefresh) begin() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.running {
		return false
	}
	q.running = true
	return true
}

func (q *quotaRefresh) end() {
	q.mu.Lock()
	q.running = false
	q.mu.Unlock()
}

// quotaClient builds a client for one login provider, with the same OAuth
// wiring the active client gets, so its usage endpoint can be asked.
func (a *App) quotaClient(info provider.Provider) (provider.Client, error) {
	a.mu.Lock()
	hook := a.quotaClientFor
	cfg := a.cfg
	store := a.store
	a.mu.Unlock()
	if hook != nil {
		return hook(info)
	}
	client, err := provider.NewClient(info.ID, provider.BaseURLFor(cfg, info.ID), provider.ResolverFor(store))
	if err != nil {
		return nil, err
	}
	if info.OAuth {
		if setter, ok := client.(interface{ SetForceKeyResolver(provider.KeyResolver) }); ok {
			setter.SetForceKeyResolver(provider.ForceResolverFor(store))
		}
		if token, ok := provider.OAuthStore(store).Load(info.ID); ok {
			if setter, ok := client.(interface{ SetAccountID(string) }); ok {
				setter.SetAccountID(token.AccountID)
			}
		}
	}
	return client, nil
}

// RefreshAllQuota asks every covered provider for its official usage and
// stores the snapshots. A provider fetched within quotaMinInterval is skipped
// unless force is set, one that fails keeps its last good figures, and one
// with no usage endpoint is remembered as such so its row says "n/a" instead
// of waiting forever. It blocks until the fetches finish, so a caller that
// must not wait runs it in a goroutine.
func (a *App) RefreshAllQuota(ctx context.Context, force bool) {
	tracker := a.QuotaTracker()
	if tracker == nil || !a.quotaRun.begin() {
		return
	}
	defer a.quotaRun.end()

	var wg sync.WaitGroup
	for _, info := range a.quotaProviders() {
		if !force {
			if snap := tracker.Snapshot(info.ID); snap != nil && len(snap.Windows) > 0 && time.Since(snap.FetchedAt) < quotaMinInterval {
				continue
			}
		}
		client, err := a.quotaClient(info)
		if err != nil || client == nil {
			continue
		}
		wg.Add(1)
		go func(info provider.Provider, client provider.Client) {
			defer wg.Done()
			fetchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			snap := client.FetchQuota(fetchCtx)
			switch {
			case snap == nil:
			case len(snap.Windows) > 0:
				tracker.RecordSnapshot(info.ID, snap)
				a.mu.Lock()
				delete(a.quotaUnavailable, info.ID)
				a.mu.Unlock()
			case snap.Unavailable != "" && tracker.Snapshot(info.ID) == nil:
				a.mu.Lock()
				if a.quotaUnavailable == nil {
					a.quotaUnavailable = map[string]bool{}
				}
				a.quotaUnavailable[info.ID] = true
				a.mu.Unlock()
			}
		}(info, client)
	}
	wg.Wait()
	a.forgetQuotaRows()
}
