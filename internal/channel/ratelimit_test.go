package channel

import (
	"testing"
	"time"
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
