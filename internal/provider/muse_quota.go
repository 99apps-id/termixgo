package provider

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// museQuotaFetcher reports the Muse-side allowance for one login. Meta
// exposes no documented usage endpoint on the Muse surface: the fetcher
// probes the small set of usage-shaped paths the backend has answered, and
// when none answers it says so instead of inventing a number. The gauge then
// falls back to the response rate-limit headers, which are Meta's own
// per-minute signal on every turn.
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

// museQuotaPaths are the usage-shaped paths probed in order. None is
// documented for Muse; the first path that answers a windowed document wins,
// and a clean miss on all of them is reported as unavailable rather than
// silent, so the operator knows the gauge is on headers.
var museQuotaPaths = []string{"/usage", "/quota", "/v1/usage"}

func (f *museQuotaFetcher) fetch(ctx context.Context) *QuotaSnapshot {
	snap := &QuotaSnapshot{Provider: "muse", FetchedAt: time.Now()}
	key := strings.TrimSpace(f.http.currentKey())
	if key == "" {
		snap.Unavailable = "no Muse login; run 'termixgo login muse'"
		return snap
	}
	headers := map[string]string{
		"Authorization": "Bearer " + key,
		"User-Agent":    museUserAgent,
		"X-Client-Id":   "tbh:tui",
		"x-api-version": "1.0.0",
		"Accept":        "application/json",
	}
	for _, path := range museQuotaPaths {
		endpoint := strings.TrimRight(f.http.baseURL, "/") + path
		body, status, err := quotaGET(ctx, f.http.http, endpoint, headers)
		if err != nil {
			continue
		}
		if status == 401 || status == 403 {
			snap.Unavailable = "login expired; run 'termixgo login muse'"
			return snap
		}
		if status < 200 || status >= 300 {
			continue
		}
		if parsed := parseMuseQuota(snap, body); parsed != nil {
			return parsed
		}
	}
	snap.Unavailable = "Meta exposes no usage endpoint on the Muse surface; gauge uses response headers"
	snap.Windows = nil
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
