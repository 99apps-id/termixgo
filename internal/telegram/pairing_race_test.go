package telegram

import (
	"context"
	"sync"
	"testing"
)

// TestConcurrentPairingStateIsRaceFree drives the pairing state from several
// handler goroutines at once, the way the poll loop does: every update is
// dispatched on its own goroutine, so /unpair, /pair and /status can overlap.
// It is written for the race detector rather than for its assertions.
//
// It failed before the pairing state was guarded, with the detector naming
// bot.go's read of the chat id on the /status path and the write on the
// /unpair path. A run under `go test -race` is what keeps it honest.
func TestConcurrentPairingStateIsRaceFree(t *testing.T) {
	api := newFakeAPI(t)
	bot := New("123:abc", newBlockingAgent())
	bot.client = api.client()
	bot.Pair(42, 7)
	bot.SetPairingCode("123456")
	// The app mirrors a new pairing back onto the running bot from the
	// callback, which is the other writer of this state.
	bot.OnPaired = func(chatID, ownerUserID int64) {
		bot.Pair(chatID, ownerUserID)
	}

	ctx := context.Background()
	ownerMessage := func(text string) *Message {
		return &Message{Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 7}, Text: text}
	}

	var wg sync.WaitGroup
	for _, text := range []string{"/status", "/status", "/model", "/help"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			for step := 0; step < 30; step++ {
				bot.handleMessage(ctx, ownerMessage(text))
			}
		}(text)
	}
	// The unpaired path reads the code and writes the pairing.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for step := 0; step < 30; step++ {
			bot.handleMessage(ctx, ownerMessage("/pair 123456"))
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for step := 0; step < 30; step++ {
			bot.handleMessage(ctx, ownerMessage("/unpair"))
		}
	}()
	wg.Wait()

	// The bot must still answer a /pair after all that churn, which proves the
	// state never ended up somewhere the handler could not read it.
	bot.handleMessage(ctx, ownerMessage("/pair 123456"))
	if chatID, owner := bot.Pairing(); chatID != 42 || owner != 7 {
		t.Errorf("the bot ended paired to chat %d owner %d, want 42 and 7", chatID, owner)
	}
}
