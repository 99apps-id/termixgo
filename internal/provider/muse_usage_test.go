package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// museUsageEvent is the closing event of a Muse stream, as captured from
// api.meta.ai.
const museUsageEvent = `{"subscription":{"tier":"27681527378179523","weekly":{"resets_at":%d,"used_percent":12.5},"window":{"resets_at":%d,"used_percent":40,"window_duration_mins":300}},"type":"response.subscription_usage"}`

func resetMuseUsage(t *testing.T) {
	t.Helper()
	museUsageStore.mu.Lock()
	museUsageStore.snap = nil
	museUsageStore.mu.Unlock()
	ClearQuotaCache("muse")
	t.Cleanup(func() {
		museUsageStore.mu.Lock()
		museUsageStore.snap = nil
		museUsageStore.mu.Unlock()
		ClearQuotaCache("muse")
	})
}

func TestMuseSubscriptionUsageBecomesQuotaWindows(t *testing.T) {
	resetMuseUsage(t)
	window := time.Now().Add(2 * time.Hour).Unix()
	week := time.Now().Add(5 * 24 * time.Hour).Unix()
	err := decodeResponsesStream("Muse", strings.NewReader(
		"data: "+fmt.Sprintf(museUsageEvent, week, window)+"\n\n"), func(StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	snap := MuseUsageSnapshot()
	if snap == nil || len(snap.Windows) != 2 {
		t.Fatalf("snapshot = %+v, want two windows", snap)
	}
	session := snap.SessionWindow()
	if session == nil || session.UsedPercent != 40 || session.ResetsAt == nil || session.ResetsAt.Unix() != window {
		t.Errorf("session window = %+v", session)
	}
	var weekly *QuotaWindow
	for index := range snap.Windows {
		if snap.Windows[index].Key == QuotaWeeklyKey {
			weekly = &snap.Windows[index]
		}
	}
	if weekly == nil || weekly.UsedPercent != 12.5 {
		t.Errorf("weekly window = %+v", weekly)
	}
}

// TestMuseUsageDoesNotReachTheAgentLoop keeps the subscription event out of
// the stream the loop sees: it is the gauge's data, not conversation.
func TestMuseUsageDoesNotReachTheAgentLoop(t *testing.T) {
	resetMuseUsage(t)
	var events int
	err := decodeResponsesStream("Muse", strings.NewReader(
		"data: "+fmt.Sprintf(museUsageEvent, time.Now().Add(time.Hour).Unix(), time.Now().Add(time.Hour).Unix())+"\n\n"),
		func(StreamEvent) error { events++; return nil })
	if err != nil || events != 0 {
		t.Fatalf("err=%v events=%d, want a silent record", err, events)
	}
}

func TestMuseUsageRolledOverWindowIsNotServed(t *testing.T) {
	resetMuseUsage(t)
	past := time.Now().Add(-time.Minute).Unix()
	recordMuseSubscription(museSubscription{Window: &museUsageWindow{ResetsAt: float64(past), UsedPercent: 90, DurationMins: 300}})
	if museUsageFresh(time.Now()) != nil {
		t.Errorf("a window past its reset time must not be reported as current usage")
	}
}

// TestMuseQuotaProbesWhenNothingIsStored proves the gauge works for a session
// that has not spoken to Meta yet: one tiny request, the reading read off its
// stream, and no second request while the reading is fresh.
func TestMuseQuotaProbesWhenNothingIsStored(t *testing.T) {
	resetMuseUsage(t)
	var requests int
	var probeModel string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		probeModel, _ = body["model"].(string)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
		fmt.Fprint(writer, "data: "+fmt.Sprintf(museUsageEvent, time.Now().Add(24*time.Hour).Unix(), time.Now().Add(time.Hour).Unix())+"\n\n")
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "muse", Label: "Muse", Kind: KindMuse, OAuth: true}, server.URL, "key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	snap := client.FetchQuota(context.Background())
	if snap == nil || snap.SessionWindow() == nil || snap.SessionWindow().UsedPercent != 40 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if probeModel != "muse-ember-1.0" {
		t.Errorf("probe model = %q, want the smallest catalogue entry", probeModel)
	}
	ClearQuotaCache("muse")
	if again := client.FetchQuota(context.Background()); again == nil || again.SessionWindow() == nil {
		t.Fatalf("second snapshot = %+v", again)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want 1: a fresh reading must not be probed again", requests)
	}
}
