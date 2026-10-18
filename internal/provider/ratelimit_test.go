package provider

import (
	"net/http"
	"testing"
	"time"
)

func TestParseAnthropicRateLimit(t *testing.T) {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-requests-limit", "1000")
	headers.Set("anthropic-ratelimit-requests-remaining", "950")
	headers.Set("anthropic-ratelimit-tokens-limit", "100000")
	headers.Set("anthropic-ratelimit-tokens-remaining", "85000")
	headers.Set("anthropic-ratelimit-tokens-reset", "2026-04-15T12:00:00Z")

	rl := ParseAnthropicRateLimit(headers)
	if rl == nil {
		t.Fatalf("expected rate limit info, got nil")
	}
	if rl.RequestsLimit != 1000 || rl.RequestsRemaining != 950 {
		t.Errorf("requests mismatch: got %d/%d, want 950/1000", rl.RequestsRemaining, rl.RequestsLimit)
	}
	if rl.TokensLimit != 100000 || rl.TokensRemaining != 85000 {
		t.Errorf("tokens mismatch: got %d/%d, want 85000/100000", rl.TokensRemaining, rl.TokensLimit)
	}
	if rl.TokensReset == nil || rl.TokensReset.Year() != 2026 {
		t.Errorf("reset time mismatch: got %v", rl.TokensReset)
	}
	if pct := rl.PercentTokensRemaining(); pct != 85.0 {
		t.Errorf("expected 85%% tokens remaining, got %.1f%%", pct)
	}
	if pct := rl.PercentRequestsRemaining(); pct != 95.0 {
		t.Errorf("expected 95%% requests remaining, got %.1f%%", pct)
	}
}

func TestParseOpenAIRateLimit(t *testing.T) {
	headers := http.Header{}
	headers.Set("x-ratelimit-limit-requests", "500")
	headers.Set("x-ratelimit-remaining-requests", "480")
	headers.Set("x-ratelimit-limit-tokens", "200000")
	headers.Set("x-ratelimit-remaining-tokens", "150000")
	headers.Set("x-ratelimit-reset-requests", "6ms")
	headers.Set("x-ratelimit-reset-tokens", "1.5s")

	rl := ParseOpenAIRateLimit(headers)
	if rl == nil {
		t.Fatalf("expected rate limit info, got nil")
	}
	if rl.RequestsLimit != 500 || rl.RequestsRemaining != 480 {
		t.Errorf("requests mismatch: got %d/%d, want 480/500", rl.RequestsRemaining, rl.RequestsLimit)
	}
	if rl.TokensLimit != 200000 || rl.TokensRemaining != 150000 {
		t.Errorf("tokens mismatch: got %d/%d, want 150000/200000", rl.TokensRemaining, rl.TokensLimit)
	}
	if rl.RequestsReset == nil {
		t.Errorf("expected RequestsReset to be parsed")
	}
	if rl.TokensReset == nil {
		t.Errorf("expected TokensReset to be parsed")
	}
	if pct := rl.PercentTokensRemaining(); pct != 75.0 {
		t.Errorf("expected 75%% tokens remaining, got %.1f%%", pct)
	}
}

func TestParseGenericRateLimitRetryAfter(t *testing.T) {
	headers := http.Header{}
	headers.Set("retry-after", "120")

	rl := ParseGenericRateLimit(headers)
	if rl == nil {
		t.Fatalf("expected rate limit info, got nil")
	}
	if rl.RetryAfter != 120*time.Second {
		t.Errorf("expected 120s retry-after, got %v", rl.RetryAfter)
	}
}
