package provider

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// agQuotaSummaryPath is the Antigravity quota summary endpoint. The registry
// in the 9router reference names it under the antigravity provider's
// transport usage config, on the daily host the chat stream uses. It answers
// groups of buckets: each group names a model family (Gemini, Claude, GPT)
// and each bucket carries a window ("5h", "daily" or "weekly"), a
// remainingFraction, and a resetTime.
const agQuotaSummaryPath = "/v1internal:retrieveUserQuotaSummary"

// antigravityQuotaFetcher reports the Antigravity-side allowance for one
// login. Only the session (5h) buckets feed the gauge; weekly buckets ride
// along for the /quota view.
type antigravityQuotaFetcher struct {
	http   *httpClient
	client *antigravityClient
}

// FetchQuota asks the quota summary endpoint for this login's buckets. force
// bypasses the 3-minute cache, which /quota uses for a manual refresh.
func (c *antigravityClient) FetchQuota(ctx context.Context) *QuotaSnapshot {
	return c.quotaFetcher().FetchQuota(ctx)
}

func (c *antigravityClient) quotaFetcher() *antigravityQuotaFetcher {
	return &antigravityQuotaFetcher{http: c.httpClient, client: c}
}

func (f *antigravityQuotaFetcher) FetchQuota(ctx context.Context) *QuotaSnapshot {
	return quotaCaches.fetch("antigravity", false, func() *QuotaSnapshot {
		return f.fetch(ctx)
	})
}

// FetchQuotaForced is the manual-refresh path: same fetch, no cache.
func (f *antigravityQuotaFetcher) FetchQuotaForced(ctx context.Context) *QuotaSnapshot {
	return quotaCaches.fetch("antigravity", true, func() *QuotaSnapshot {
		return f.fetch(ctx)
	})
}

func (f *antigravityQuotaFetcher) fetch(ctx context.Context) *QuotaSnapshot {
	snap := &QuotaSnapshot{Provider: "antigravity", FetchedAt: time.Now()}
	key := strings.TrimSpace(f.http.currentKey())
	if key == "" {
		snap.Unavailable = "no Antigravity login; run 'termixgo login antigravity'"
		return snap
	}
	project := f.projectID(ctx)
	headers := map[string]string{
		"Authorization":   "Bearer " + key,
		"User-Agent":      agUserAgent,
		"Content-Type":    "application/json",
		"X-Client-Name":   "antigravity",
		"X-Client-Version": "2.11.0",
	}
	payload := "{}"
	if strings.TrimSpace(project) != "" {
		encoded, err := json.Marshal(map[string]any{"project": project})
		if err == nil {
			payload = string(encoded)
		}
	}
	endpoint := strings.TrimRight(f.http.baseURL, "/") + agQuotaSummaryPath
	body, status, err := quotaPOST(ctx, f.http.http, endpoint, headers, payload)
	if err != nil {
		snap.Unavailable = "quota endpoint unreachable: " + err.Error()
		return snap
	}
	if status == 401 || status == 403 {
		snap.Unavailable = "login expired; run 'termixgo login antigravity'"
		return snap
	}
	if status < 200 || status >= 300 {
		snap.Unavailable = "quota endpoint answered " + httpStatusText(status)
		return snap
	}
	return parseAntigravityQuota(snap, body)
}

// projectID reuses the login's project through the live client. A fetcher
// without one sends no project and the backend answers for the token's
// default project.
func (f *antigravityQuotaFetcher) projectID(ctx context.Context) string {
	if f.client == nil {
		return ""
	}
	project, err := f.client.ensureProject(ctx)
	if err != nil || strings.TrimSpace(project) == "" {
		return ""
	}
	return project
}

// parseAntigravityQuota turns a retrieveUserQuotaSummary document into
// windows. Groups may live at data.groups or data.quotaSummary.groups; each
// group names a family (Gemini vs Claude/GPT) and each bucket a window. The
// 5h buckets feed the gauge; weekly buckets ride along for the /quota view.
// A disabled session bucket means the weekly allowance was hit: it is kept
// with zero remaining so the gauge reads honestly instead of vanishing.
func parseAntigravityQuota(snap *QuotaSnapshot, body []byte) *QuotaSnapshot {
	var doc struct {
		Groups []struct {
			DisplayName string `json:"displayName"`
			Buckets     []struct {
				BucketID          string  `json:"bucketId"`
				DisplayName       string  `json:"displayName"`
				Window            string  `json:"window"`
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         any     `json:"resetTime"`
				Disabled          bool    `json:"disabled"`
			} `json:"buckets"`
		} `json:"groups"`
		QuotaSummary struct {
			Groups []struct {
				DisplayName string `json:"displayName"`
				Buckets     []struct {
					BucketID          string  `json:"bucketId"`
					DisplayName       string  `json:"displayName"`
					Window            string  `json:"window"`
					RemainingFraction *float64 `json:"remainingFraction"`
					ResetTime         any     `json:"resetTime"`
					Disabled          bool    `json:"disabled"`
				} `json:"buckets"`
			} `json:"groups"`
		} `json:"quotaSummary"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		snap.Unavailable = "quota response unreadable"
		snap.Windows = nil
		return snap
	}
	groups := doc.Groups
	if len(groups) == 0 {
		for _, group := range doc.QuotaSummary.Groups {
			groups = append(groups, struct {
				DisplayName string `json:"displayName"`
				Buckets     []struct {
					BucketID          string  `json:"bucketId"`
					DisplayName       string  `json:"displayName"`
					Window            string  `json:"window"`
					RemainingFraction *float64 `json:"remainingFraction"`
					ResetTime         any     `json:"resetTime"`
					Disabled          bool    `json:"disabled"`
				} `json:"buckets"`
			}(group))
		}
	}
	var windows []QuotaWindow
	for _, group := range groups {
		family := antigravityFamily(group.DisplayName)
		if family == "" {
			continue
		}
		for _, bucket := range group.Buckets {
			window := antigravityBucketWindow(bucket.Window, bucket.BucketID, bucket.DisplayName)
			if window == "" {
				continue
			}
			if window == QuotaWeeklyKey && bucket.Disabled {
				continue
			}
			var used float64
			if bucket.Disabled {
				used = 100
			} else {
				if bucket.RemainingFraction == nil {
					continue
				}
				used = clampPercent(100 - *bucket.RemainingFraction*100)
			}
			key := window
			display := "Session (5h)"
			if window == QuotaWeeklyKey {
				display = "Weekly (7d)"
			}
			if family != "default" {
				key = family + "_" + window
				familyDisplay := map[string]string{"gemini": "Gemini", "claude_gpt": "Claude & GPT"}[family]
				if window == QuotaWeeklyKey {
					display = familyDisplay + " Weekly (7d)"
				} else {
					display = familyDisplay + " (5h)"
				}
			}
			if hasQuotaKey(windows, key) {
				continue
			}
			windows = append(windows, QuotaWindow{
				Key:         key,
				DisplayName: display,
				UsedPercent: clampPercent(used),
				ResetsAt:    quotaTime(bucket.ResetTime),
			})
		}
	}
	if len(windows) == 0 {
		snap.Unavailable = "no quota buckets in quota response"
		snap.Windows = nil
		return snap
	}
	snap.Windows = windows
	return snap
}

// antigravityFamily maps a group display name to a stable family key.
func antigravityFamily(displayName string) string {
	lowered := strings.ToLower(displayName)
	switch {
	case strings.Contains(lowered, "gemini"):
		return "gemini"
	case strings.Contains(lowered, "claude") || strings.Contains(lowered, "gpt"):
		return "claude_gpt"
	default:
		return ""
	}
}

// antigravityBucketWindow maps a bucket window to a stable key: "5h" and
// "daily" both mean the session window, "weekly" the weekly one. Anything
// else is not a window the gauge knows, so it is skipped rather than mapped
// to a guess.
func antigravityBucketWindow(window, bucketID, displayName string) string {
	lowered := strings.ToLower(strings.TrimSpace(window))
	if lowered == "weekly" || strings.Contains(strings.ToLower(bucketID+" "+displayName), "weekly") {
		return QuotaWeeklyKey
	}
	combined := strings.ToLower(window + " " + bucketID + " " + displayName)
	switch {
	case lowered == "5h" || lowered == "daily":
		return QuotaSessionKey
	case strings.Contains(combined, "five hour") || strings.Contains(combined, "5h"):
		return QuotaSessionKey
	default:
		return ""
	}
}

func hasQuotaKey(windows []QuotaWindow, key string) bool {
	for i := range windows {
		if windows[i].Key == key {
			return true
		}
	}
	return false
}
