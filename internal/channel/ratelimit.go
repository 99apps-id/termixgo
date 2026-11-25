package channel

import (
	"context"
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

// GuestRateLimits maintains independent inbound and outbound per-guest quotas
// for one account. Create one instance per running account, not per message.
// Callers must bypass both checks for operators and stop/control paths.
type GuestRateLimits struct {
	inbound  *RateLimiter
	outbound *RateLimiter
	notices  *RateLimiter
}

// NewGuestRateLimits applies the account's effective guest settings. Inbound
// defaults to six messages per minute; outbound defaults to twelve.
func NewGuestRateLimits(policy config.ChannelAccount) *GuestRateLimits {
	limits := policy.EffectiveGuestLimits()
	return &GuestRateLimits{
		inbound:  NewRateLimiter(limits.RatePerMinute),
		outbound: NewRateLimiter(limits.OutboundRatePerMinute),
		notices:  NewRateLimiter(1),
	}
}

// AllowNotice avoids turning inbound spam into repeated outbound warnings.
func (l *GuestRateLimits) AllowNotice(guestID string, now time.Time) bool {
	return l == nil || l.notices.Allow(guestID, now)
}

// AllowInbound consumes one inbound message allowance for guestID. Identity
// must be the stable authenticated sender ID, not user-supplied text.
func (l *GuestRateLimits) AllowInbound(guestID string, now time.Time) bool {
	if l == nil {
		return true
	}
	return l.inbound.Allow(guestID, now)
}

// AllowOutbound consumes one outbound message allowance for guestID. Call once
// per actual transport message, including each chunk, before sending it.
func (l *GuestRateLimits) AllowOutbound(guestID string, now time.Time) bool {
	if l == nil {
		return true
	}
	return l.outbound.Allow(guestID, now)
}

type guestOutboundKey struct{}
type guestOutbound struct {
	limiter *GuestRateLimits
	id      string
}

// WithGuestOutbound binds one authenticated guest to outbound transport calls.
func WithGuestOutbound(ctx context.Context, limiter *GuestRateLimits, guestID string) context.Context {
	return context.WithValue(ctx, guestOutboundKey{}, guestOutbound{limiter: limiter, id: guestID})
}

// WaitGuestOutbound waits for one outgoing-message slot rather than silently
// discarding a response. Untagged admin and control requests bypass the quota.
// Cancellation stops the wait; callers still need durable delivery if a
// response must survive a process restart.
func WaitGuestOutbound(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	guest, ok := ctx.Value(guestOutboundKey{}).(guestOutbound)
	if !ok {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if guest.limiter.AllowOutbound(guest.id, time.Now()) {
			return nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
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

// gcLocked drops the senders whose every hit has left the window, now and
// then, so a long-running account does not keep one slice per stranger. An
// entry is never empty while it is stored, so the test has to look at the
// newest hit rather than the length.
func (l *RateLimiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < 10*time.Minute {
		return
	}
	l.lastGC = now
	cutoff := now.Add(-l.window)
	for sender, stamps := range l.hits {
		if len(stamps) == 0 || !stamps[len(stamps)-1].After(cutoff) {
			delete(l.hits, sender)
		}
	}
}
