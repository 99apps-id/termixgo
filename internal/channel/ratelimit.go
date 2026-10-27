package channel

import (
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

// RateLimiter bounds how fast one sender may post. A guest chat is an open
// channel to a paid model, so a single sender must not be able to run it flat
// out.
type RateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	limit  int
	hits   map[string][]time.Time
	lastGC time.Time
}

// NewRateLimiter builds a limiter for one account. A limit of zero or less
// means the feature is off, which is what an admin chat wants.
func NewRateLimiter(perMinute int) *RateLimiter {
	return &RateLimiter{window: time.Minute, limit: perMinute, hits: map[string][]time.Time{}}
}

// ForAccount builds the limiter one account's guest settings ask for.
func ForAccount(policy config.ChannelAccount) *RateLimiter {
	return NewRateLimiter(policy.EffectiveGuestLimits().RatePerMinute)
}

// Allow reports whether this message may run. The caller answers a refused
// message with one friendly line, never silence: the sender knows they are
// being throttled, not ignored.
func (l *RateLimiter) Allow(senderID string, now time.Time) bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	kept := l.hits[senderID][:0]
	for _, stamp := range l.hits[senderID] {
		if stamp.After(cutoff) {
			kept = append(kept, stamp)
		}
	}
	if len(kept) >= l.limit {
		l.hits[senderID] = kept
		return false
	}
	l.hits[senderID] = append(kept, now)
	l.gcLocked(now)
	return true
}

// gcLocked drops empty sender entries now and then, so a long-running account
// does not accumulate one slice per stranger.
func (l *RateLimiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < 10*time.Minute {
		return
	}
	l.lastGC = now
	for sender, stamps := range l.hits {
		if len(stamps) == 0 {
			delete(l.hits, sender)
		}
	}
}
