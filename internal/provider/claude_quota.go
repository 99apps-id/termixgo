package provider

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// claudeOAuthUsageURL is the Claude Code OAuth usage endpoint. The registry in
// the 9router reference names it under the claude provider's transport usage
// config. It answers the windows this login may use: five_hour is the 5-hour
// session, seven_day the weekly allowance, and seven_day_<model> plus
// limits[] entries the model-scoped weekly windows. utilization is percent
// USED, so the remaining fraction is 100 minus that.
const claudeOAuthUsageURL = "https://api.anthropic.com/api/oauth/usage"

// claudeQuotaFetcher reports the Claude-side allowance for one OAuth login.
// API-key logins have no self-serve usage endpoint: their gauge stays on the
// response rate-limit headers, which is the provider's own per-minute signal.
type claudeQuotaFetcher struct {
	http *httpClient
}

// FetchQuota asks the OAuth usage endpoint for this login's windows. force
// bypasses the 3-minute cache, which /quota uses for a manual refresh.
func (c *anthropicClient) FetchQuota(ctx context.Context) *QuotaSnapshot {
	return c.quotaFetcher().FetchQuota(ctx)
}

func (c *anthropicClient) quotaFetcher() *claudeQuotaFetcher {
	return &claudeQuotaFetcher{http: c.httpClient}
}

func (f *claudeQuotaFetcher) FetchQuota(ctx context.Context) *QuotaSnapshot {
	return quotaCaches.fetch(f.http.info.ID, false, func() *QuotaSnapshot {
		return f.fetch(ctx)
	})
}

// FetchQuotaForced is the manual-refresh path: same fetch, no cache.
func (f *claudeQuotaFetcher) FetchQuotaForced(ctx context.Context) *QuotaSnapshot {
	return quotaCaches.fetch(f.http.info.ID, true, func() *QuotaSnapshot {
		return f.fetch(ctx)
	})
}

func (f *claudeQuotaFetcher) fetch(ctx context.Context) *QuotaSnapshot {
	providerID := f.http.info.ID
	snap := &QuotaSnapshot{Provider: providerID, FetchedAt: time.Now()}
	if !f.http.info.OAuth {
		snap.Unavailable = "usage endpoint needs a Claude OAuth login, not an API key"
		return snap
	}
	key := strings.TrimSpace(f.http.currentKey())
	if key == "" {
		snap.Unavailable = "no Claude login; run 'termixgo login " + providerID + "'"
		return snap
	}
	headers := map[string]string{
		"Authorization":   "Bearer " + key,
		"anthropic-beta":  "oauth-2025-04-20",
		"anthropic-version": "2023-06-01",
		"Accept":          "application/json",
	}
	body, status, err := quotaGET(ctx, f.http.http, claudeOAuthUsageURL, headers)
	if err != nil {
		snap.Unavailable = "usage endpoint unreachable: " + err.Error()
		return snap
	}
	if status == 401 || status == 403 {
		snap.Unavailable = "login expired; run 'termixgo login " + providerID + "'"
		return snap
	}
	if status == 429 {
		snap.Unavailable = "usage endpoint rate limited; retry shortly"
		return snap
	}
	if status < 200 || status >= 300 {
		snap.Unavailable = "usage endpoint answered " + httpStatusText(status)
		return snap
	}
	return parseClaudeUsage(snap, body)
}

// parseClaudeUsage turns an OAuth usage document into windows. five_hour is
// the 5-hour session; seven_day the weekly allowance; seven_day_<model> keys
// and limits[] entries with kind weekly_scoped the model-scoped weekly
// windows. A window without a utilization number is not a window: omitting it
// keeps the gauge from inventing a zero.
func parseClaudeUsage(snap *QuotaSnapshot, body []byte) *QuotaSnapshot {
	var doc struct {
		FiveHour  map[string]any `json:"five_hour"`
		SevenDay  map[string]any `json:"seven_day"`
		Limits    []struct {
			Kind    string         `json:"kind"`
			Percent *float64       `json:"percent"`
			Resets  any            `json:"resets_at"`
			Scope   map[string]any `json:"scope"`
		} `json:"limits"`
		ExtraUsage any `json:"extra_usage"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		snap.Unavailable = "usage response unreadable"
		snap.Windows = nil
		return snap
	}
	payload, _ := json.Marshal(raw)
	decoder = json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	_ = decoder.Decode(&doc)

	var windows []QuotaWindow
	if window := claudeUtilWindow(QuotaSessionKey, "Session (5h)", doc.FiveHour); window != nil {
		windows = append(windows, *window)
	}
	if window := claudeUtilWindow(QuotaWeeklyKey, "Weekly (7d)", doc.SevenDay); window != nil {
		windows = append(windows, *window)
	}
	// Model-scoped weekly windows arrive as seven_day_<model> keys.
	for key, value := range raw {
		if !strings.HasPrefix(key, "seven_day_") || key == "seven_day" {
			continue
		}
		window, ok := value.(map[string]any)
		if !ok {
			continue
		}
		model := strings.TrimPrefix(key, "seven_day_")
		if model == "" {
			continue
		}
		if row := claudeUtilWindow("weekly_"+model, "Weekly "+model+" (7d)", window); row != nil {
			windows = append(windows, *row)
		}
	}
	// Newer model-scoped weekly limits arrive in limits[] instead.
	for _, limit := range doc.Limits {
		if limit.Kind != "weekly_scoped" || limit.Percent == nil {
			continue
		}
		model := claudeLimitModel(limit.Scope)
		if model == "" {
			continue
		}
		used := clampPercent(*limit.Percent)
		windows = append(windows, QuotaWindow{
			Key:         "weekly_" + model,
			DisplayName: "Weekly " + model + " (7d)",
			UsedPercent: used,
			ResetsAt:    quotaTime(limit.Resets),
		})
	}
	if len(windows) == 0 {
		snap.Unavailable = "no quota windows in usage response"
		snap.Windows = nil
		return snap
	}
	snap.Windows = windows
	return snap
}

// claudeUtilWindow reads one utilization window: utilization is percent USED.
func claudeUtilWindow(key, display string, window map[string]any) *QuotaWindow {
	if len(window) == 0 {
		return nil
	}
	used, ok := quotaNumber(window["utilization"])
	if !ok {
		return nil
	}
	return &QuotaWindow{
		Key:         key,
		DisplayName: display,
		UsedPercent: clampPercent(used),
		ResetsAt:    quotaTime(window["resets_at"], window["resetsAt"], window["reset_at"], window["resetAt"]),
	}
}

// claudeLimitModel reads the display name out of a limits[] scope entry:
// { scope: { model: { display_name: "Fable" } } }.
func claudeLimitModel(scope map[string]any) string {
	if len(scope) == 0 {
		return ""
	}
	model, ok := scope["model"].(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"display_name", "displayName", "name"} {
		if name, ok := model[key].(string); ok && strings.TrimSpace(name) != "" {
			return strings.ToLower(strings.TrimSpace(name))
		}
	}
	return ""
}
