package channel

import (
	"strings"
	"testing"
	"time"
)

func TestRateLimiterForgetsSendersWhoWentQuiet(t *testing.T) {
	limiter := NewRateLimiter(3)
	start := time.Now()
	for index := 0; index < 200; index++ {
		limiter.Allow("stranger-"+string(rune('a'+index%26))+strings.Repeat("x", index), start)
	}
	// A later message, past both the window and the sweep interval, sweeps.
	limiter.Allow("someone", start.Add(time.Hour))
	if got := len(limiter.hits); got != 1 {
		t.Fatalf("%d sender entries kept, want 1: quiet strangers must not pile up", got)
	}
}

func TestPairingRequestsExpireAndAreCapped(t *testing.T) {
	store, err := OpenPairingStore("")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaxPendingPairings+10; index++ {
		store.Request("telegram", "default", "user-"+strings.Repeat("9", index+1), "")
	}
	if got := len(store.Pending()); got != MaxPendingPairings {
		t.Fatalf("%d pending, want the cap %d", got, MaxPendingPairings)
	}
	// Age every request past the TTL.
	store.mu.Lock()
	for index := range store.requests {
		store.requests[index].CreatedAt = time.Now().Add(-PairingTTL - time.Minute)
	}
	store.mu.Unlock()
	if got := len(store.Pending()); got != 0 {
		t.Fatalf("%d stale requests survived", got)
	}
	if request, created := store.Request("telegram", "default", "fresh", ""); !created || request.Code == "------" {
		t.Fatalf("a new sender must get a real code once the old ones expired: %+v", request)
	}
}

func TestRedactCoversOAuthTokensStoredAsJSON(t *testing.T) {
	stored := `{"access":"ya29.a0AfH6SMBexampleexampleexample12345","refresh":"1//0gExampleRefreshTokenValue0123456789abcdefgh","accountId":"x"}`
	known := SecretValues(stored)
	answer := "the token is ya29.a0AfH6SMBexampleexampleexample12345 and the refresh is 1//0gExampleRefreshTokenValue0123456789abcdefgh"
	redacted := Redact(answer, known)
	for _, leaked := range []string{"ya29.", "1//0g"} {
		if strings.Contains(redacted, leaked) {
			t.Errorf("%q leaked: %s", leaked, redacted)
		}
	}
	// The patterns alone catch them too, for a token the store never held.
	if got := Redact("Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123", nil); strings.Contains(got, "abcdefghijkl") {
		t.Errorf("a bearer value leaked: %s", got)
	}
	if got := Redact("client GOCSPX-abcdefghijklmnopqrstuv", nil); strings.Contains(got, "GOCSPX-abc") {
		t.Errorf("a client secret leaked: %s", got)
	}
}
