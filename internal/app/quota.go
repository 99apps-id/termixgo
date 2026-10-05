package app

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/provider"
)

// RollingWindow is the 5-hour rolling quota window standard for coding providers.
const RollingWindow = 5 * time.Hour

// UsageRecord logs one turn's accounting.
type UsageRecord struct {
	Timestamp        time.Time
	Provider         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
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

// ActiveQuotaGauge returns a compact gauge string for the header or status.
// It prefers the provider's official quota snapshot when available: the
// 5-hour session window renders "[████░░] 78%" and the reset countdown
// appends "(3h12m)". Otherwise it falls back to live rate-limit headers,
// then to the local 5h activity estimate "[5h: 12r/42k]".
func (q *QuotaTracker) ActiveQuotaGauge(providerName string, width int) string {
	if q == nil {
		return ""
	}
	if snap := q.Snapshot(providerName); snap != nil {
		if w := snap.SessionWindow(); w != nil {
			remaining := 100 - w.UsedPercent
			g := VisualGauge(remaining, width)
			if w.ResetsAt != nil {
				left := time.Until(*w.ResetsAt).Round(time.Minute)
				if left > 0 {
					return g + fmt.Sprintf("(%s)", left)
				}
			}
			return g
		}
	}
	stats := q.Stats(providerName, RollingWindow)
	if stats.RateLimit != nil && stats.RateLimit.TokensLimit > 0 {
		pct := stats.RateLimit.PercentTokensRemaining()
		if pct >= 0 {
			return VisualGauge(pct, width)
		}
	}
	if stats.Requests > 0 {
		return fmt.Sprintf("[5h: %dr/%dk]", stats.Requests, (stats.TotalTokens+500)/1000)
	}
	return ""
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
