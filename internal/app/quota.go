package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// RollingWindow is the 5-hour rolling quota window standard for coding providers.
const RollingWindow = 5 * time.Hour

// UsageRecord logs one turn's accounting.
type UsageRecord struct {
	Timestamp        time.Time `json:"ts"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt"`
	CompletionTokens int       `json:"completion"`
	TotalTokens      int       `json:"total"`
}

// WindowStats summarizes activity in a rolling window.
type WindowStats struct {
	Provider         string
	Window           time.Duration
	Requests         int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	OldestInWindow   time.Time
	ResetIn          time.Duration
	RateLimit        *provider.RateLimitInfo
}

// QuotaTracker maintains 5-hour rolling usage, live provider rate limits,
// and official provider quota snapshots fetched from usage endpoints.
type QuotaTracker struct {
	mu         sync.RWMutex
	records    []UsageRecord
	rateLimits map[string]*provider.RateLimitInfo // key: provider
	snapshots  map[string]*provider.QuotaSnapshot // key: provider
	// statePath, when set, is where the rolling window survives a restart.
	// The tracker used to live only in memory, so a restart in the middle of
	// a provider's 5-hour window reset the local gauge to zero and only an
	// official snapshot, when its fetch succeeded, covered the gap.
	statePath string
}

// NewQuotaTracker builds a new quota tracker.
func NewQuotaTracker() *QuotaTracker {
	return &QuotaTracker{
		rateLimits: make(map[string]*provider.RateLimitInfo),
		snapshots:  make(map[string]*provider.QuotaSnapshot),
	}
}

// RecordUsage records one completed turn or subagent run.
func (q *QuotaTracker) RecordUsage(providerName, model string, usage provider.Usage) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()
	q.records = append(q.records, UsageRecord{
		Timestamp:        now,
		Provider:         strings.ToLower(strings.TrimSpace(providerName)),
		Model:            strings.TrimSpace(model),
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
	})

	// Prune records older than 24 hours to keep memory compact.
	cutoff := now.Add(-24 * time.Hour)
	start := 0
	for start < len(q.records) && q.records[start].Timestamp.Before(cutoff) {
		start++
	}
	if start > 0 {
		q.records = q.records[start:]
	}
	// Best effort: the gauge must never fail a turn over its own state file.
	_ = q.saveLocked(now)
}

// quotaState is the persisted shape: the records still inside the rolling
// window, nothing else. Rate limits and official snapshots are live data and
// are re-fetched, never revived from disk.
type quotaState struct {
	Records []UsageRecord `json:"records"`
}

// SetStatePath points the tracker at its state file. Call LoadState after
// setting it. An empty path turns persistence off.
func (q *QuotaTracker) SetStatePath(path string) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.statePath = path
}

// LoadState restores the rolling window from the state file. Records that
// have aged out of the 5-hour window are dropped on load: reviving them
// would inflate a window they no longer belong to. A missing file is an
// empty window, not an error.
func (q *QuotaTracker) LoadState() error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.statePath == "" {
		return nil
	}
	data, err := os.ReadFile(q.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read quota state: %w", err)
	}
	var state quotaState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parse quota state: %w", err)
	}
	cutoff := time.Now().Add(-RollingWindow)
	kept := state.Records[:0]
	for _, record := range state.Records {
		if record.Timestamp.After(cutoff) {
			kept = append(kept, record)
		}
	}
	q.records = append(q.records, kept...)
	return nil
}

// saveLocked writes the in-window records to the state file. The caller
// holds the lock. Only the rolling window is persisted: older records are
// pruned from memory at 24 hours but would be dropped on load anyway, and
// keeping the file to one window keeps it small.
func (q *QuotaTracker) saveLocked(now time.Time) error {
	if q.statePath == "" {
		return nil
	}
	cutoff := now.Add(-RollingWindow)
	state := quotaState{}
	for _, record := range q.records {
		if record.Timestamp.After(cutoff) {
			state.Records = append(state.Records, record)
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(q.statePath), 0o700); err != nil {
		return fmt.Errorf("create quota state directory: %w", err)
	}
	tmp := q.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write quota state: %w", err)
	}
	if err := secrets.RestrictFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("protect quota state: %w", err)
	}
	if err := os.Rename(tmp, q.statePath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace quota state: %w", err)
	}
	return secrets.RestrictFile(q.statePath)
}

// RecordRateLimit records live headers from a provider response.
func (q *QuotaTracker) RecordRateLimit(providerName string, rl *provider.RateLimitInfo) {
	if q == nil || rl == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(providerName))
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rateLimits[key] = rl
}

// RecordSnapshot stores an official provider quota snapshot.
// A nil snapshot clears the stored one. Snapshots are the provider's own
// account of usage and take precedence over local estimates in gauges.
func (q *QuotaTracker) RecordSnapshot(providerName string, snap *provider.QuotaSnapshot) {
	if q == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(providerName))
	if key == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if snap == nil {
		delete(q.snapshots, key)
		return
	}
	q.snapshots[key] = snap
}

// Snapshot returns the stored official quota snapshot for a provider, if any.
func (q *QuotaTracker) Snapshot(providerName string) *provider.QuotaSnapshot {
	if q == nil {
		return nil
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.snapshots[strings.ToLower(strings.TrimSpace(providerName))]
}

// Stats returns the rolling window summary for a specific provider.
func (q *QuotaTracker) Stats(providerName string, window time.Duration) WindowStats {
	if q == nil {
		return WindowStats{Provider: providerName, Window: window}
	}
	q.mu.RLock()
	defer q.mu.RUnlock()

	target := strings.ToLower(strings.TrimSpace(providerName))
	now := time.Now()
	cutoff := now.Add(-window)

	var oldest time.Time
	reqs := 0
	prompt := 0
	comp := 0
	total := 0

	for _, r := range q.records {
		if r.Timestamp.After(cutoff) {
			if target == "" || r.Provider == target {
				if oldest.IsZero() || r.Timestamp.Before(oldest) {
					oldest = r.Timestamp
				}
				reqs++
				prompt += r.PromptTokens
				comp += r.CompletionTokens
				total += r.TotalTokens
			}
		}
	}

	var resetIn time.Duration
	if !oldest.IsZero() {
		expiresAt := oldest.Add(window)
		if expiresAt.After(now) {
			resetIn = expiresAt.Sub(now)
		}
	}

	rl := q.rateLimits[target]
	return WindowStats{
		Provider:         providerName,
		Window:           window,
		Requests:         reqs,
		PromptTokens:     prompt,
		CompletionTokens: comp,
		TotalTokens:      total,
		OldestInWindow:   oldest,
		ResetIn:          resetIn,
		RateLimit:        rl,
	}
}

// AllTrackedProviders returns all provider names with activity or rate limits.
func (q *QuotaTracker) AllTrackedProviders() []string {
	if q == nil {
		return nil
	}
	q.mu.RLock()
	defer q.mu.RUnlock()

	seen := make(map[string]bool)
	var list []string
	for _, r := range q.records {
		if r.Provider != "" && !seen[r.Provider] {
			seen[r.Provider] = true
			list = append(list, r.Provider)
		}
	}
	for p := range q.rateLimits {
		if p != "" && !seen[p] {
			seen[p] = true
			list = append(list, p)
		}
	}
	for p, snap := range q.snapshots {
		if p != "" && !seen[p] && snap != nil && len(snap.Windows) > 0 {
			seen[p] = true
			list = append(list, p)
		}
	}
	return list
}

// AllStats returns rolling window summaries for all tracked providers.
func (q *QuotaTracker) AllStats(window time.Duration) []WindowStats {
	providers := q.AllTrackedProviders()
	var list []WindowStats
	for _, p := range providers {
		list = append(list, q.Stats(p, window))
	}
	return list
}

// LocalEstimateLabel marks numbers that come from the local rolling window
// rather than from the provider. It is spelled out on every surface that
// shows the fallback counts, so a local count is never read as an official
// quota figure.
const LocalEstimateLabel = "local estimate, not official provider figure"

// SnapshotAgeText renders how long ago a quota snapshot was fetched, the
// marker that keeps a cached snapshot from reading as live data. An empty
// string means the snapshot carries no fetch time.
func SnapshotAgeText(fetchedAt time.Time) string {
	if fetchedAt.IsZero() {
		return ""
	}
	return fmt.Sprintf("fetched %s ago", time.Since(fetchedAt).Round(time.Second))
}

// QuotaRef names the official quota window a call against this provider and
// model draws on, for stamping usage ledger records. The session window
// applies to every model, so it wins; a plain weekly window is next; a
// model-scoped window (weekly_sonnet and kin) is named only when the model
// matches its suffix, because naming another model's window would attribute
// the spend to an allowance it never touched. No snapshot, no name: the
// empty string leaves the record's quota_ref null rather than inventing a
// window.
func (q *QuotaTracker) QuotaRef(providerName, model string) string {
	snap := q.Snapshot(providerName)
	if snap == nil || len(snap.Windows) == 0 {
		return ""
	}
	pick := snap.SessionWindow()
	if pick == nil {
		for i := range snap.Windows {
			if snap.Windows[i].Key == provider.QuotaWeeklyKey {
				pick = &snap.Windows[i]
				break
			}
		}
	}
	if pick == nil {
		lowerModel := strings.ToLower(strings.TrimSpace(model))
		for i := range snap.Windows {
			key := snap.Windows[i].Key
			idx := strings.Index(key, "_")
			if idx < 0 || idx+1 >= len(key) {
				continue
			}
			if strings.Contains(lowerModel, key[idx+1:]) {
				pick = &snap.Windows[i]
				break
			}
		}
	}
	if pick == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(providerName)) + ":" + pick.Key
}

// ActiveQuotaGauge returns a compact gauge string for the header or status.
// Bars always mean used, on every surface: when the provider's official
// quota snapshot is available its session window renders the used percent
// ("[██░░░░░░] 22%"), the reset countdown appends "(3h12m)" and the
// snapshot's age follows so a cached figure is not read as live. Otherwise
// it falls back to live rate-limit headers (also as used), then to the
// local 5h activity counts, which carry no bar and are labelled as a local
// estimate.
func (q *QuotaTracker) ActiveQuotaGauge(providerName string, width int) string {
	if q == nil {
		return ""
	}
	if snap := q.Snapshot(providerName); snap != nil {
		if w := snap.SessionWindow(); w != nil {
			g := VisualGauge(w.UsedPercent, width)
			if w.ResetsAt != nil {
				left := time.Until(*w.ResetsAt).Round(time.Minute)
				if left > 0 {
					g += fmt.Sprintf("(%s)", left)
				}
			}
			if age := SnapshotAgeText(snap.FetchedAt); age != "" {
				g += " " + age
			}
			return g
		}
	}
	stats := q.Stats(providerName, RollingWindow)
	if stats.RateLimit != nil && stats.RateLimit.TokensLimit > 0 {
		if remaining := stats.RateLimit.PercentTokensRemaining(); remaining >= 0 {
			return VisualGauge(100-remaining, width)
		}
	}
	// For OAuth providers or providers with an official snapshot history,
	// the local 5-hour window estimate acts as a fallback.
	// For standard API key / local models, showing "[5h: ...]" is misleading
	// because they do not have a 5-hour subscription quota window.
	if (isOAuthProvider(providerName) || q.Snapshot(providerName) != nil) && stats.Requests > 0 {
		return fmt.Sprintf("[5h: %dr/%dk] %s", stats.Requests, (stats.TotalTokens+500)/1000, LocalEstimateLabel)
	}
	return ""
}

func isOAuthProvider(providerName string) bool {
	norm := strings.ToLower(strings.TrimSpace(providerName))
	if norm == "codex" || norm == "openai-codex" || norm == "antigravity" || norm == "muse" || norm == "github-copilot" {
		return true
	}
	if strings.Contains(norm, "oauth") {
		return true
	}
	if p, ok := provider.ByID(norm); ok && p.OAuth {
		return true
	}
	return false
}

// VisualGauge renders an ASCII progress bar of given width for 0-100 percentage.
func VisualGauge(percent float64, width int) string {
	if percent < 0 {
		return "[?]"
	}
	if width < 5 {
		width = 10
	}
	filled := int((percent / 100.0) * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("[%s] %.0f%%", bar, percent)
}
