package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// museUsageWindow is one allowance window of Meta's subscription usage event.
// Meta reports it in the stream itself: the last event of every response is
// response.subscription_usage, carrying the 5-hour window and the weekly one
// with their used percentage and reset time. There is no separate usage
// endpoint on the Muse surface, which is why the CLI's own usage command can
// only echo what the last response said.
type museUsageWindow struct {
	ResetsAt     float64 `json:"resets_at"`
	UsedPercent  float64 `json:"used_percent"`
	DurationMins float64 `json:"window_duration_mins"`
}

type museSubscription struct {
	Tier   string           `json:"tier"`
	Window *museUsageWindow `json:"window"`
	Weekly *museUsageWindow `json:"weekly"`
}

// museUsageMaxAge is how long a reading is trusted before a tiny probe asks
// again. Every real turn refreshes the reading for free, so the probe only
// matters for an idle session whose figures have gone stale.
const museUsageMaxAge = 15 * time.Minute

var museUsageStore struct {
	mu   sync.Mutex
	snap *QuotaSnapshot
}

// snapshot turns the event into the shared quota shape. A window with no
// reset time is kept: the figure is still the provider's own.
func (s museSubscription) snapshot(now time.Time) *QuotaSnapshot {
	snap := &QuotaSnapshot{Provider: "muse", FetchedAt: now}
	add := func(window *museUsageWindow, key, display string) {
		if window == nil {
			return
		}
		item := QuotaWindow{Key: key, DisplayName: display, UsedPercent: clampPercent(window.UsedPercent)}
		if window.ResetsAt > 0 {
			reset := time.Unix(int64(window.ResetsAt), 0)
			item.ResetsAt = &reset
		}
		snap.Windows = append(snap.Windows, item)
	}
	if s.Window != nil {
		// The short window is the 5-hour session unless Meta says another length.
		if s.Window.DurationMins == 0 || s.Window.DurationMins == 300 {
			add(s.Window, QuotaSessionKey, "Session (5h)")
		} else {
			add(s.Window, "window_"+formatMinutes(s.Window.DurationMins), "Window ("+formatMinutes(s.Window.DurationMins)+")")
		}
	}
	add(s.Weekly, QuotaWeeklyKey, "Weekly (7d)")
	return snap
}

func formatMinutes(minutes float64) string {
	total := int(minutes)
	if total >= 60 && total%60 == 0 {
		return strconv.Itoa(total/60) + "h"
	}
	return strconv.Itoa(total) + "m"
}

// recordMuseSubscription stores a reading taken from a response stream. It is
// what makes the gauge move with every turn without a request of its own.
func recordMuseSubscription(sub museSubscription) {
	snap := sub.snapshot(time.Now())
	if len(snap.Windows) == 0 {
		return
	}
	museUsageStore.mu.Lock()
	museUsageStore.snap = snap
	museUsageStore.mu.Unlock()
	quotaCaches.store("muse", snap)
}

// museUsageFresh returns the stored reading when it is recent and no window
// of it has already rolled over.
func museUsageFresh(now time.Time) *QuotaSnapshot {
	museUsageStore.mu.Lock()
	defer museUsageStore.mu.Unlock()
	snap := museUsageStore.snap
	if snap == nil || now.Sub(snap.FetchedAt) > museUsageMaxAge {
		return nil
	}
	for _, window := range snap.Windows {
		if window.ResetsAt != nil && now.After(*window.ResetsAt) {
			return nil
		}
	}
	return snap
}

// museProbeModels are tried in order for the usage probe: the smallest
// catalogue entry first, then the flagship for an account that lacks it.
var museProbeModels = []string{"muse-ember-1.0", "muse-spark-1.3"}

// probeUsage asks for the smallest response the backend will give and
// reads the subscription event off its end. It spends a few dozen tokens, so
// it runs only when no recent reading exists.
func (c *museClient) probeUsage(ctx context.Context) (*QuotaSnapshot, error) {
	var lastErr error
	for _, model := range museProbeModels {
		payload := map[string]any{
			"model":             model,
			"input":             []map[string]any{{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "."}}}},
			"stream":            true,
			"store":             false,
			"max_output_tokens": 16,
			"reasoning":         map[string]any{"effort": "minimal"},
		}
		response, err := c.send(ctx, c.currentKey(), payload)
		if err != nil {
			lastErr = err
			continue
		}
		found := drainMuseUsage(response)
		if found != nil {
			return found, nil
		}
	}
	return nil, lastErr
}

// drainMuseUsage reads a probe response to its end and returns the reading
// its subscription event carried, or nil when it carried none.
func drainMuseUsage(response *http.Response) *QuotaSnapshot {
	defer response.Body.Close()
	reader := newSSEReader(io.LimitReader(response.Body, 1<<20))
	for {
		payload, err := reader.next()
		if err != nil {
			return nil
		}
		var event responsesEvent
		if jsonErr := json.Unmarshal([]byte(payload), &event); jsonErr != nil || event.Type != "response.subscription_usage" || event.Subscription == nil {
			continue
		}
		recordMuseSubscription(*event.Subscription)
		return museUsageFresh(time.Now())
	}
}

// MuseUsageSnapshot is the usage Meta last sent in a response stream, or nil
// when there is none or it is stale. The app reads it at the end of a turn so
// the gauge moves with the turn instead of waiting for the next poll.
func MuseUsageSnapshot() *QuotaSnapshot {
	return museUsageFresh(time.Now())
}
