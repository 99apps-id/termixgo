package provider

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// codexUsageURL is the ChatGPT backend usage endpoint. The registry in the
// 9router reference names it under the codex provider's transport usage
// config, and the response carries rate_limit windows with used_percent and
// reset_at fields.
const codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// codexQuotaFetcher reports the ChatGPT-side Codex allowance for one login:
// the primary window is the 5-hour session, the secondary the weekly one.
type codexQuotaFetcher struct {
	http    *httpClient
	account string
}

// FetchCodexQuota asks the ChatGPT backend for this login's usage. force
// bypasses the 3-minute cache, which /quota uses for a manual refresh.
func (c *codexClient) FetchQuota(ctx context.Context) *QuotaSnapshot {
	return c.quotaFetcher().FetchQuota(ctx)
}

func (c *codexClient) quotaFetcher() *codexQuotaFetcher {
	return &codexQuotaFetcher{http: c.httpClient, account: c.accountID}
}

func (f *codexQuotaFetcher) FetchQuota(ctx context.Context) *QuotaSnapshot {
	return quotaCaches.fetch("openai-codex", false, func() *QuotaSnapshot {
		return f.fetch(ctx)
	})
}

// FetchQuotaForced is the manual-refresh path: same fetch, no cache.
func (f *codexQuotaFetcher) FetchQuotaForced(ctx context.Context) *QuotaSnapshot {
	return quotaCaches.fetch("openai-codex", true, func() *QuotaSnapshot {
		return f.fetch(ctx)
	})
}

func (f *codexQuotaFetcher) fetch(ctx context.Context) *QuotaSnapshot {
	snap := &QuotaSnapshot{Provider: "openai-codex", FetchedAt: time.Now()}
	key := strings.TrimSpace(f.http.currentKey())
	if key == "" {
		snap.Unavailable = "no Codex login; run 'termixgo login openai-codex'"
		return snap
	}
	headers := map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
		"originator":    "codex_cli_rs",
		"User-Agent":    "codex_cli_rs/" + codexCLIVersion,
	}
	if strings.TrimSpace(f.account) != "" {
		headers["ChatGPT-Account-ID"] = strings.TrimSpace(f.account)
	}
	body, status, err := quotaGET(ctx, f.http.http, codexUsageURL, headers)
	if err != nil {
		snap.Unavailable = "usage endpoint unreachable: " + err.Error()
		return snap
	}
	if status == 401 || status == 403 {
		snap.Unavailable = "login expired; run 'termixgo login openai-codex'"
		return snap
	}
	if status < 200 || status >= 300 {
		snap.Unavailable = "usage endpoint answered " + httpStatusText(status)
		return snap
	}
	return parseCodexUsage(snap, body)
}

// parseCodexUsage turns a /wham/usage document into windows. The document
// carries its windows under rate_limit (or the whole document is the window
// map): primary_window is the 5-hour session, secondary_window the weekly
// allowance. Each window reports used_percent and reset_at.
func parseCodexUsage(snap *QuotaSnapshot, body []byte) *QuotaSnapshot {
	var doc struct {
		RateLimit map[string]any `json:"rate_limit"`
		Primary   map[string]any `json:"primary_window"`
		Secondary map[string]any `json:"secondary_window"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		snap.Unavailable = "usage response unreadable"
		snap.Windows = nil
		return snap
	}
	windows := codexWindowsFromMap(doc.RateLimit)
	if len(windows) == 0 {
		raw := map[string]any{}
		decoder = json.NewDecoder(strings.NewReader(string(body)))
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err == nil {
			windows = codexWindowsFromMap(raw)
		}
	}
	// A bare primary/secondary at the top level is the same shape one level up.
	if len(windows) == 0 && (len(doc.Primary) > 0 || len(doc.Secondary) > 0) {
		windows = codexWindowsFromMap(map[string]any{
			"primary_window":   doc.Primary,
			"secondary_window": doc.Secondary,
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

// codexWindowsFromMap reads primary/secondary windows out of one level of a
// usage document. Either key spelling is accepted because the endpoint has
// answered both across backend versions.
func codexWindowsFromMap(level map[string]any) []QuotaWindow {
	if len(level) == 0 {
		return nil
	}
	var out []QuotaWindow
	if window := codexWindow(level, "primary_window", "primary", QuotaSessionKey, "Session (5h)"); window != nil {
		out = append(out, *window)
	}
	if window := codexWindow(level, "secondary_window", "secondary", QuotaWeeklyKey, "Weekly (7d)"); window != nil {
		out = append(out, *window)
	}
	return out
}

// codexWindow reads one window under either of two key spellings. A window
// without a used fraction is not a window: omitting it keeps the gauge from
// inventing a zero.
func codexWindow(level map[string]any, keys ...string) *QuotaWindow {
	var primary, fallback, key, display string
	if len(keys) == 4 {
		primary, fallback, key, display = keys[0], keys[1], keys[2], keys[3]
	} else {
		return nil
	}
	raw, ok := level[primary]
	if !ok {
		raw, ok = level[fallback]
	}
	if !ok {
		return nil
	}
	window, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	used, ok := quotaNumber(window["used_percent"], window["percent_used"])
	if !ok {
		return nil
	}
	return &QuotaWindow{
		Key:         key,
		DisplayName: display,
		UsedPercent: clampPercent(used),
		ResetsAt:    quotaTime(window["reset_at"], window["resets_at"], window["resetAt"]),
	}
}

// quotaNumber reads the first finite number from its candidates.
func quotaNumber(candidates ...any) (float64, bool) {
	for _, raw := range candidates {
		switch value := raw.(type) {
		case json.Number:
			if parsed, err := value.Float64(); err == nil && finite(parsed) {
				return parsed, true
			}
		case float64:
			if finite(value) {
				return value, true
			}
		case int64:
			return float64(value), true
		case int:
			return float64(value), true
		}
	}
	return 0, false
}

func finite(value float64) bool { return value == value && value < 1e308 && value > -1e308 }

// quotaTime accepts several raw values and returns the first timestamp
// any of them yields.
func quotaTime(candidates ...any) *time.Time {
	for _, raw := range candidates {
		if moment := parseQuotaTime(raw); moment != nil {
			return moment
		}
	}
	return nil
}

func httpStatusText(status int) string {
	if status <= 0 {
		return "no response"
	}
	return strings.TrimSpace(strings.Join([]string{"HTTP", itoa(status)}, " "))
}

func itoa(number int) string {
	if number == 0 {
		return "0"
	}
	negative := number < 0
	if negative {
		number = -number
	}
	var digits []byte
	for number > 0 {
		digits = append([]byte{byte('0' + number%10)}, digits...)
		number /= 10
	}
	if negative {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
