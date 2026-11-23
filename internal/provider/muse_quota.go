package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// museQuotaFetcher reports the Muse-side allowance for one login, read from
// the subscription usage event Meta ends every response with.
type museQuotaFetcher struct {
	http *httpClient
}

// FetchQuota asks the Muse backend for this login's windows. force bypasses
// the 3-minute cache, which /quota uses for a manual refresh.
func (c *museClient) FetchQuota(ctx context.Context) *QuotaSnapshot {
	return c.quotaFetcher().FetchQuota(ctx)
}

func (c *museClient) quotaFetcher() *museQuotaFetcher {
	return &museQuotaFetcher{http: c.httpClient}
}

func (f *museQuotaFetcher) FetchQuota(ctx context.Context) *QuotaSnapshot {
	return quotaCaches.fetch("muse", false, func() *QuotaSnapshot {
		return f.fetch(ctx)
	})
}

// FetchQuotaForced is the manual-refresh path: same fetch, no cache.
func (f *museQuotaFetcher) FetchQuotaForced(ctx context.Context) *QuotaSnapshot {
	return quotaCaches.fetch("muse", true, func() *QuotaSnapshot {
		return f.fetch(ctx)
	})
}

// fetch reports the usage Meta last sent. The reading rides the end of every
// response stream, so a session that has spoken to Meta already has one; only
// an idle or fresh session probes, with a request of a few dozen tokens.
func (f *museQuotaFetcher) fetch(ctx context.Context) *QuotaSnapshot {
	snap := &QuotaSnapshot{Provider: "muse", FetchedAt: time.Now()}
	if strings.TrimSpace(f.http.currentKey()) == "" {
		snap.Unavailable = "no Muse login; run 'termixgo login muse'"
		return snap
	}
	if fresh := museUsageFresh(time.Now()); fresh != nil {
		return fresh
	}
	probed, err := (&museClient{httpClient: f.http}).probeUsage(ctx)
	if probed != nil {
		return probed
	}
	if err != nil {
		var status *providerStatusError
		if errors.As(err, &status) && (status.status == 401 || status.status == 403) {
			snap.Unavailable = "login expired; run 'termixgo login muse'"
			return snap
		}
		snap.Unavailable = "Muse usage unavailable: " + err.Error()
		return snap
	}
	snap.Unavailable = "Meta sent no subscription usage with the last response"
	return snap
}

// parseMuseQuota accepts a windowed usage document in the common shapes:
// { windows: [...] }, { quotas: {...} }, or a bare primary/secondary map.
// Anything else answers nil so the next path is probed. A window without a
// used fraction is not a window.
func parseMuseQuota(snap *QuotaSnapshot, body []byte) *QuotaSnapshot {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil || len(raw) == 0 {
		return nil
	}
	var candidates []map[string]any
	if windows, ok := raw["windows"].([]any); ok {
		for _, entry := range windows {
			if window, ok := entry.(map[string]any); ok {
				candidates = append(candidates, window)
			}
		}
	}
	if quotas, ok := raw["quotas"].(map[string]any); ok {
		for key, entry := range quotas {
			if window, ok := entry.(map[string]any); ok {
				window["_key"] = key
				candidates = append(candidates, window)
			}
		}
	}
	if len(candidates) == 0 {
		candidates = append(candidates, raw)
	}
	var out []QuotaWindow
	for _, window := range candidates {
		used, ok := quotaNumber(window["used_percent"], window["percent_used"], window["utilization"], window["used"])
		if !ok {
			continue
		}
		key := museWindowKey(window)
		display := museWindowDisplay(key)
		out = append(out, QuotaWindow{
			Key:         key,
			DisplayName: display,
			UsedPercent: clampPercent(used),
			ResetsAt:    quotaTime(window["reset_at"], window["resets_at"], window["resetAt"], window["resetsAt"], window["reset_time"], window["resetTime"]),
		})
	}
	if len(out) == 0 {
		return nil
	}
	snap.Windows = out
	return snap
}

// museWindowKey maps a window's own name to a stable key. Unknown names keep
// a sanitised form of themselves so a new provider window still shows rather
// than vanishing.
func museWindowKey(window map[string]any) string {
	for _, field := range []string{"key", "id", "name", "window", "type"} {
		if name, ok := window[field].(string); ok {
			lowered := strings.ToLower(strings.TrimSpace(name))
			switch {
			case strings.Contains(lowered, "5h") || strings.Contains(lowered, "five") || lowered == "primary" || lowered == "session":
				return QuotaSessionKey
			case strings.Contains(lowered, "week") || lowered == "secondary":
				return QuotaWeeklyKey
			case lowered != "":
				return "window_" + strings.Map(sanitiseQuotaKey, lowered)
			}
		}
	}
	if _, ok := window["_key"].(string); ok {
		if name, ok := window["_key"].(string); ok && strings.TrimSpace(name) != "" {
			return "window_" + strings.Map(sanitiseQuotaKey, strings.ToLower(strings.TrimSpace(name)))
		}
	}
	return QuotaSessionKey
}

func museWindowDisplay(key string) string {
	switch key {
	case QuotaSessionKey:
		return "Session (5h)"
	case QuotaWeeklyKey:
		return "Weekly (7d)"
	default:
		return strings.TrimPrefix(key, "window_")
	}
}

func sanitiseQuotaKey(runeValue rune) rune {
	if runeValue >= 'a' && runeValue <= 'z' || runeValue >= '0' && runeValue <= '9' || runeValue == '_' {
		return runeValue
	}
	return '_'
}
