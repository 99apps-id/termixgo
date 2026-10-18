package provider

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// EventRateLimit carries rate limit and quota information from the provider.
const EventRateLimit StreamEventType = "rate-limit"

// RateLimitInfo carries quota / rate limit headers reported by the provider.
type RateLimitInfo struct {
	RequestsLimit     int
	RequestsRemaining int
	RequestsReset     *time.Time
	TokensLimit       int
	TokensRemaining   int
	TokensReset       *time.Time
	RetryAfter        time.Duration
}

// PercentTokensRemaining returns remaining tokens percentage 0-100, or -1 if unknown.
func (r *RateLimitInfo) PercentTokensRemaining() float64 {
	if r == nil || r.TokensLimit <= 0 {
		return -1
	}
	pct := (float64(r.TokensRemaining) / float64(r.TokensLimit)) * 100.0
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// PercentRequestsRemaining returns remaining requests percentage 0-100, or -1 if unknown.
func (r *RateLimitInfo) PercentRequestsRemaining() float64 {
	if r == nil || r.RequestsLimit <= 0 {
		return -1
	}
	pct := (float64(r.RequestsRemaining) / float64(r.RequestsLimit)) * 100.0
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// ParseAnthropicRateLimit extracts Anthropic rate limit headers.
func ParseAnthropicRateLimit(headers http.Header) *RateLimitInfo {
	if headers == nil {
		return nil
	}
	info := &RateLimitInfo{}
	found := false

	if val := getHeaderFold(headers, "anthropic-ratelimit-requests-limit"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			info.RequestsLimit = n
			found = true
		}
	}
	if val := getHeaderFold(headers, "anthropic-ratelimit-requests-remaining"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			info.RequestsRemaining = n
			found = true
		}
	}
	if val := getHeaderFold(headers, "anthropic-ratelimit-requests-reset"); val != "" {
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			info.RequestsReset = &t
			found = true
		}
	}

	if val := getHeaderFold(headers, "anthropic-ratelimit-tokens-limit"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			info.TokensLimit = n
			found = true
		}
	}
	if val := getHeaderFold(headers, "anthropic-ratelimit-tokens-remaining"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			info.TokensRemaining = n
			found = true
		}
	}
	if val := getHeaderFold(headers, "anthropic-ratelimit-tokens-reset"); val != "" {
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			info.TokensReset = &t
			found = true
		}
	}

	if ra := parseRetryAfter(getHeaderFold(headers, "retry-after")); ra > 0 {
		info.RetryAfter = ra
		found = true
	}

	if !found {
		return nil
	}
	return info
}

// ParseOpenAIRateLimit extracts OpenAI and Codex x-ratelimit-* headers.
func ParseOpenAIRateLimit(headers http.Header) *RateLimitInfo {
	if headers == nil {
		return nil
	}
	info := &RateLimitInfo{}
	found := false

	if val := getHeaderFold(headers, "x-ratelimit-limit-requests"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			info.RequestsLimit = n
			found = true
		}
	}
	if val := getHeaderFold(headers, "x-ratelimit-remaining-requests"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			info.RequestsRemaining = n
			found = true
		}
	}
	if val := getHeaderFold(headers, "x-ratelimit-reset-requests"); val != "" {
		if d, err := parseOpenAIReset(val); err == nil {
			t := time.Now().Add(d)
			info.RequestsReset = &t
			found = true
		}
	}

	if val := getHeaderFold(headers, "x-ratelimit-limit-tokens"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			info.TokensLimit = n
			found = true
		}
	}
	if val := getHeaderFold(headers, "x-ratelimit-remaining-tokens"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			info.TokensRemaining = n
			found = true
		}
	}
	if val := getHeaderFold(headers, "x-ratelimit-reset-tokens"); val != "" {
		if d, err := parseOpenAIReset(val); err == nil {
			t := time.Now().Add(d)
			info.TokensReset = &t
			found = true
		}
	}

	if ra := parseRetryAfter(getHeaderFold(headers, "retry-after")); ra > 0 {
		info.RetryAfter = ra
		found = true
	}

	if !found {
		return nil
	}
	return info
}

// ParseGenericRateLimit checks for common rate limit or retry-after headers.
func ParseGenericRateLimit(headers http.Header) *RateLimitInfo {
	if headers == nil {
		return nil
	}
	if info := ParseOpenAIRateLimit(headers); info != nil {
		return info
	}
	if info := ParseAnthropicRateLimit(headers); info != nil {
		return info
	}
	if ra := parseRetryAfter(getHeaderFold(headers, "retry-after")); ra > 0 {
		return &RateLimitInfo{RetryAfter: ra}
	}
	return nil
}

func getHeaderFold(headers http.Header, name string) string {
	target := strings.ToLower(name)
	for k, v := range headers {
		if strings.ToLower(k) == target && len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
	}
	return ""
}

func parseOpenAIReset(val string) (time.Duration, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// e.g. "6ms", "1s", "2m30s", "100ms"
	d, err := time.ParseDuration(val)
	if err == nil {
		return d, nil
	}
	// might be seconds as float or integer: "0.5", "12"
	if f, err := strconv.ParseFloat(val, 64); err == nil && f > 0 {
		return time.Duration(f * float64(time.Second)), nil
	}
	return 0, err
}
