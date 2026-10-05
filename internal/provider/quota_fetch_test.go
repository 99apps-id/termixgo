package provider

import (
	"strings"
	"testing"
	"time"
)

func TestParseCodexUsagePrimarySecondary(t *testing.T) {
	snap := &QuotaSnapshot{Provider: "openai-codex", FetchedAt: time.Now()}
	body := `{"rate_limit": {"primary_window": {"used_percent": 62.5, "reset_at": 1780000000}, "secondary_window": {"used_percent": 10, "reset_at": 1780600000}}}`
	parsed := parseCodexUsage(snap, []byte(body))
	if parsed.Unavailable != "" {
		t.Fatalf("Unavailable = %q", parsed.Unavailable)
	}
	session := parsed.SessionWindow()
	if session == nil {
		t.Fatal("no session window")
	}
	if session.UsedPercent != 62.5 {
		t.Fatalf("UsedPercent = %v, want 62.5", session.UsedPercent)
	}
	if session.ResetsAt == nil {
		t.Fatal("session ResetsAt nil")
	}
	if len(parsed.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(parsed.Windows))
	}
}

func TestParseCodexUsageNoWindows(t *testing.T) {
	snap := &QuotaSnapshot{Provider: "openai-codex", FetchedAt: time.Now()}
	parsed := parseCodexUsage(snap, []byte(`{"plan_type": "plus"}`))
	if parsed.Unavailable == "" {
		t.Fatal("expected Unavailable when no windows present")
	}
	if parsed.Windows != nil {
		t.Fatalf("Windows = %v, want nil so the gauge never reads zero", parsed.Windows)
	}
}

func TestParseClaudeUsageFiveHour(t *testing.T) {
	snap := &QuotaSnapshot{Provider: "claude-oauth", FetchedAt: time.Now()}
	body := `{"five_hour": {"utilization": 37, "resets_at": "2026-10-06T12:00:00Z"}, "seven_day": {"utilization": 12, "resets_at": "2026-10-12T12:00:00Z"}}`
	parsed := parseClaudeUsage(snap, []byte(body))
	if parsed.Unavailable != "" {
		t.Fatalf("Unavailable = %q", parsed.Unavailable)
	}
	session := parsed.SessionWindow()
	if session == nil {
		t.Fatal("no session window")
	}
	if session.UsedPercent != 37 {
		t.Fatalf("UsedPercent = %v, want 37 (utilization is percent USED)", session.UsedPercent)
	}
	if session.ResetsAt == nil {
		t.Fatal("session ResetsAt nil")
	}
}

func TestParseClaudeUsageWeeklyScoped(t *testing.T) {
	snap := &QuotaSnapshot{Provider: "claude-oauth", FetchedAt: time.Now()}
	body := `{"five_hour": {"utilization": 5, "resets_at": "2026-10-06T12:00:00Z"}, "limits": [{"kind": "weekly_scoped", "percent": 80, "resets_at": "2026-10-12T00:00:00Z", "scope": {"model": {"display_name": "Fable"}}}]}`
	parsed := parseClaudeUsage(snap, []byte(body))
	if parsed.Unavailable != "" {
		t.Fatalf("Unavailable = %q", parsed.Unavailable)
	}
	found := false
	for _, window := range parsed.Windows {
		if strings.Contains(window.Key, "fable") {
			found = true
			if window.UsedPercent != 80 {
				t.Fatalf("fable UsedPercent = %v, want 80", window.UsedPercent)
			}
		}
	}
	if !found {
		t.Fatalf("no fable window in %v", parsed.Windows)
	}
}

func TestParseAntigravityQuotaSession(t *testing.T) {
	snap := &QuotaSnapshot{Provider: "antigravity", FetchedAt: time.Now()}
	body := `{"groups": [{"displayName": "Gemini", "buckets": [{"bucketId": "default", "displayName": "Five hour", "window": "5h", "remainingFraction": 0.42, "resetTime": "2026-10-06T12:00:00Z"}]}, {"displayName": "Claude", "buckets": [{"bucketId": "default", "displayName": "Weekly", "window": "weekly", "remainingFraction": 0.9, "resetTime": "2026-10-12T00:00:00Z"}]}]}`
	parsed := parseAntigravityQuota(snap, []byte(body))
	if parsed.Unavailable != "" {
		t.Fatalf("Unavailable = %q", parsed.Unavailable)
	}
	var session *QuotaWindow
	for i := range parsed.Windows {
		if strings.HasSuffix(parsed.Windows[i].Key, "session") {
			session = &parsed.Windows[i]
		}
	}
	if session == nil {
		t.Fatalf("no session window in %v", parsed.Windows)
	}
	if session.UsedPercent < 57 || session.UsedPercent > 59 {
		t.Fatalf("UsedPercent = %v, want ~58 (100 - 42)", session.UsedPercent)
	}
}

func TestParseAntigravityQuotaDisabledSession(t *testing.T) {
	snap := &QuotaSnapshot{Provider: "antigravity", FetchedAt: time.Now()}
	body := `{"groups": [{"displayName": "Gemini", "buckets": [{"bucketId": "default", "displayName": "Five hour", "window": "5h", "remainingFraction": 0.5, "disabled": true}]}]}`
	parsed := parseAntigravityQuota(snap, []byte(body))
	if parsed.Unavailable != "" {
		t.Fatalf("Unavailable = %q", parsed.Unavailable)
	}
	var session *QuotaWindow
	for i := range parsed.Windows {
		if strings.HasSuffix(parsed.Windows[i].Key, "session") {
			session = &parsed.Windows[i]
		}
	}
	if session == nil {
		t.Fatal("disabled session bucket must be kept so the gauge reads 0, not vanish")
	}
	if session.UsedPercent != 100 {
		t.Fatalf("UsedPercent = %v, want 100 for a disabled session bucket", session.UsedPercent)
	}
}

func TestParseMuseQuotaHonest(t *testing.T) {
	snap := &QuotaSnapshot{Provider: "muse", FetchedAt: time.Now()}
	if parsed := parseMuseQuota(snap, []byte(`{"ok": true}`)); parsed != nil {
		t.Fatalf("expected nil for a windowless document, got %v", parsed.Windows)
	}
	parsed := parseMuseQuota(snap, []byte(`{"windows": [{"key": "session", "used_percent": 25, "reset_at": "2026-10-06T12:00:00Z"}]}`))
	if parsed == nil || parsed.SessionWindow() == nil {
		t.Fatal("expected a session window from a windowed document")
	}
}

func TestQuotaTimeShapes(t *testing.T) {
	if quotaTime("2026-10-06T12:00:00Z") == nil {
		t.Fatal("RFC3339 not parsed")
	}
	if quotaTime(float64(1780000000)) == nil {
		t.Fatal("epoch seconds not parsed")
	}
	if quotaTime(float64(1780000000000)) == nil {
		t.Fatal("epoch millis not parsed")
	}
	if quotaTime("garbage") != nil {
		t.Fatal("garbage must not parse")
	}
	if quotaTime(nil) != nil {
		t.Fatal("nil must not parse")
	}
}
