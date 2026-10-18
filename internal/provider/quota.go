package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// QuotaWindow is one provider-side usage window: a named allowance with a
// used fraction and a reset time. The names follow the providers' own
// vocabulary: Codex calls them primary (5h) and secondary (weekly),
// Anthropic calls them five_hour and seven_day, and Antigravity reports them
// as buckets whose window reads "5h", "daily" or "weekly".
type QuotaWindow struct {
	// Key is the stable local id: "session" for the 5-hour window, "weekly"
	// for the 7-day window, or a model-scoped variant such as
	// "weekly_sonnet".
	Key string
	// DisplayName is the operator-facing label, e.g. "Session (5h)".
	DisplayName string
	// UsedPercent is 0-100 of the allowance consumed.
	UsedPercent float64
	// ResetsAt is when the window rolls over, when the provider says.
	ResetsAt *time.Time
}

// QuotaSnapshot is the provider's own account of usage: one optional window
// per allowance it reports. A nil Windows slice means the fetch failed or the
// provider offers no usage endpoint; it never means zero usage.
type QuotaSnapshot struct {
	Provider    string
	FetchedAt   time.Time
	Windows     []QuotaWindow
	Unavailable string
}

// SessionWindow returns the 5-hour window, or nil when absent.
func (s *QuotaSnapshot) SessionWindow() *QuotaWindow {
	if s == nil {
		return nil
	}
	for i := range s.Windows {
		if s.Windows[i].Key == QuotaSessionKey {
			return &s.Windows[i]
		}
	}
	return nil
}

const (
	// QuotaSessionKey is the 5-hour rolling window.
	QuotaSessionKey = "session"
	// QuotaWeeklyKey is the 7-day window.
	QuotaWeeklyKey = "weekly"
)

// QuotaFetcher reports the provider-side usage for one login. It is a small
// interface so each vendor's endpoint quirks stay in its own file, and so
// tests can substitute a stub without network.
type QuotaFetcher interface {
	// FetchQuota asks the provider how much of each allowance this login has
	// used. A failed fetch returns a snapshot carrying Unavailable, never an
	// empty window list that would read as zero usage.
	FetchQuota(ctx context.Context) *QuotaSnapshot
}

// quotaCacheTTL bounds how often a usage endpoint is hit: the providers rate
// limit these endpoints (Anthropic answers 429), and every tab refreshing at
// once would trip that. A manual /quota refresh bypasses the cache.
const quotaCacheTTL = 3 * time.Minute

// quotaCache is the per-process cache of fetched snapshots, keyed by
// provider id. AFetcher stores through it; the app clears it on /quota.
type quotaCache struct {
	mu        sync.Mutex
	snapshots map[string]*QuotaSnapshot
	inflight  map[string]*quotaCall
}

type quotaCall struct {
	done chan struct{}
	snap *QuotaSnapshot
}

var quotaCaches = &quotaCache{
	snapshots: make(map[string]*QuotaSnapshot),
	inflight:  make(map[string]*quotaCall),
}

// FetchQuota returns the cached snapshot when fresh, coalesces concurrent
// callers onto one request, and stores the result. force skips the cache.
func (q *quotaCache) fetch(providerID string, force bool, fetch func() *QuotaSnapshot) *QuotaSnapshot {
	q.mu.Lock()
	if !force {
		if snap, ok := q.snapshots[providerID]; ok && snap != nil {
			if time.Since(snap.FetchedAt) < quotaCacheTTL {
				q.mu.Unlock()
				return snap
			}
		}
		if call, ok := q.inflight[providerID]; ok {
			q.mu.Unlock()
			<-call.done
			return call.snap
		}
	}
	call := &quotaCall{done: make(chan struct{})}
	q.inflight[providerID] = call
	q.mu.Unlock()

	snap := fetch()

	q.mu.Lock()
	delete(q.inflight, providerID)
	if snap != nil && snap.Unavailable == "" {
		q.snapshots[providerID] = snap
	}
	call.snap = snap
	close(call.done)
	q.mu.Unlock()
	return snap
}

// ClearQuotaCache drops the cached snapshot for a provider, so the next read
// hits the endpoint again.
func ClearQuotaCache(providerID string) {
	quotaCaches.mu.Lock()
	defer quotaCaches.mu.Unlock()
	delete(quotaCaches.snapshots, strings.ToLower(strings.TrimSpace(providerID)))
}

// clampPercent keeps a provider's used fraction inside 0-100: a value outside
// the range is a malformed response, not infinite usage.
func clampPercent(value float64) float64 {
	if value != value {
		return 0
	}
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

// parseQuotaTime accepts the timestamp shapes the usage endpoints use: RFC3339
// strings, epoch seconds, and epoch milliseconds.
func parseQuotaTime(raw any) *time.Time {
	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil
		}
		if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
			return &parsed
		}
		var epoch float64
		if _, err := fmt.Sscanf(trimmed, "%f", &epoch); err == nil && epoch > 0 {
			return quotaTimeFromEpoch(epoch)
		}
		return nil
	case float64:
		if value <= 0 {
			return nil
		}
		return quotaTimeFromEpoch(value)
	case int64:
		if value <= 0 {
			return nil
		}
		return quotaTimeFromEpoch(float64(value))
	case json.Number:
		if epoch, err := value.Float64(); err == nil && epoch > 0 {
			return quotaTimeFromEpoch(epoch)
		}
		return nil
	default:
		return nil
	}
}

// quotaTimeFromEpoch interprets a number as seconds or milliseconds: anything
// above 1e12 must be milliseconds, since seconds will not reach that for
// millennia.
func quotaTimeFromEpoch(epoch float64) *time.Time {
	var moment time.Time
	if epoch > 1e12 {
		seconds := int64(epoch / 1000)
		nanos := int64((epoch - float64(seconds)*1000) * 1e6)
		moment = time.Unix(seconds, nanos)
	} else {
		moment = time.Unix(int64(epoch), 0)
	}
	moment = moment.UTC()
	return &moment
}

// quotaGET performs one authenticated usage fetch. It never retries: a usage
// read must not cost the operator a turn's worth of waiting, and a failed
// read degrades to the local gauge rather than an error.
func quotaGET(ctx context.Context, client *http.Client, url string, headers map[string]string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<14))
		response.Body.Close()
	}()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	return body, response.StatusCode, nil
}

// quotaPOST is quotaGET with a JSON body, for the Antigravity quota summary
// endpoint which takes its project in the body.
func quotaPOST(ctx context.Context, client *http.Client, url string, headers map[string]string, body string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<14))
		response.Body.Close()
	}()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	return payload, response.StatusCode, nil
}
