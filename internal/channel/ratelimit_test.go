package channel

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestRateLimiterBoundsOneSender(t *testing.T) {
	limiter := NewRateLimiter(2)
	start := time.Now()

	if !limiter.Allow("guest", start) || !limiter.Allow("guest", start.Add(time.Second)) {
		t.Fatalf("the first two messages must pass")
	}
	if limiter.Allow("guest", start.Add(2*time.Second)) {
		t.Errorf("a third message inside the window must be refused")
	}
	if !limiter.Allow("other", start.Add(2*time.Second)) {
		t.Errorf("one sender's limit must not throttle another")
	}
	if !limiter.Allow("guest", start.Add(61*time.Second)) {
		t.Errorf("a message after the window must pass again")
	}
}

func TestGuestRateLimitsDirectionsAndGuests(t *testing.T) {
	limiter := NewGuestRateLimits(config.ChannelAccount{Guest: &config.GuestLimits{
		RatePerMinute: 2, OutboundRatePerMinute: 1,
	}})
	now := time.Now()
	if !limiter.AllowInbound("a", now) || !limiter.AllowInbound("a", now) || limiter.AllowInbound("a", now) {
		t.Fatal("inbound quota not enforced")
	}
	if !limiter.AllowOutbound("a", now) || limiter.AllowOutbound("a", now) {
		t.Fatal("outbound quota not independent or not enforced")
	}
	if !limiter.AllowInbound("b", now) || !limiter.AllowOutbound("b", now) {
		t.Fatal("one guest affected another")
	}
	if !limiter.AllowInbound("a", now.Add(time.Minute)) || !limiter.AllowOutbound("a", now.Add(time.Minute)) {
		t.Fatal("quota did not expire at the window boundary")
	}
}

func TestGuestRateLimitsDefaultsAndDisabled(t *testing.T) {
	now := time.Now()
	l := NewGuestRateLimits(config.ChannelAccount{})
	for i := 0; i < config.DefaultGuestRatePerMinute; i++ {
		if !l.AllowInbound("a", now) {
			t.Fatal("default inbound refused early")
		}
	}
	if l.AllowInbound("a", now) {
		t.Fatal("default inbound was not limited")
	}
	for i := 0; i < config.DefaultGuestOutboundRatePerMinute; i++ {
		if !l.AllowOutbound("a", now) {
			t.Fatal("default outbound refused early")
		}
	}
	if l.AllowOutbound("a", now) {
		t.Fatal("default outbound was not limited")
	}
	l = NewGuestRateLimits(config.ChannelAccount{Guest: &config.GuestLimits{RatePerMinute: -1, OutboundRatePerMinute: -1}})
	for i := 0; i < 100; i++ {
		if !l.AllowInbound("a", now) || !l.AllowOutbound("a", now) {
			t.Fatal("disabled limit refused")
		}
	}
	var none *GuestRateLimits
	if !none.AllowInbound("a", now) || !none.AllowOutbound("a", now) {
		t.Fatal("nil limiter refused")
	}
}

func TestGuestRateLimitsConcurrent(t *testing.T) {
	l := NewGuestRateLimits(config.ChannelAccount{Guest: &config.GuestLimits{OutboundRatePerMinute: 3}})
	now := time.Now()
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.AllowOutbound("a", now) {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 3 {
		t.Fatalf("allowed %d, want 3", got)
	}
}

func TestRateLimiterCanBeOff(t *testing.T) {
	limiter := NewRateLimiter(0)
	now := time.Now()
	for index := 0; index < 100; index++ {
		if !limiter.Allow("guest", now) {
			t.Fatalf("an unlimited limiter refused a message")
		}
	}
	if !(*RateLimiter)(nil).Allow("guest", now) {
		t.Errorf("a nil limiter must allow, which is what an admin chat uses")
	}
}
