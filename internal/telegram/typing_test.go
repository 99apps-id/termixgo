package telegram

import (
	"context"
	"testing"
	"time"
)

// TestTypingIndicatorIsRenewedWhileTheRunContinues is the operator-visible
// point of the keepalive.
//
// Telegram clears a chat action after about five seconds, so a turn that ran
// for a minute showed the typing indicator for the first few seconds and
// nothing afterwards, which read as the bot having stopped. The indicator is
// now re-sent until the turn returns.
func TestTypingIndicatorIsRenewedWhileTheRunContinues(t *testing.T) {
	previous := typingRefresh
	typingRefresh = 20 * time.Millisecond
	t.Cleanup(func() { typingRefresh = previous })

	api := newRecordingAPI(t)
	agent := newBlockingAgent()
	bot := New("123:abc", agent)
	bot.client = api.client()

	done := make(chan struct{})
	go func() {
		defer close(done)
		bot.runPrompt(context.Background(), 42, "a long task")
	}()

	select {
	case <-agent.started:
	case <-time.After(5 * time.Second):
		t.Fatalf("the run never started")
	}

	// Give the keepalive several intervals to fire while the turn is still in
	// flight.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(api.calls("sendChatAction")) < 3 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(api.calls("sendChatAction")); got < 3 {
		t.Errorf("sendChatAction called %d times during a long turn, want the typing action renewed", got)
	}

	close(agent.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("the turn did not finish after the agent was released")
	}

	// Once the turn ends the keepalive must stop, so a finished run does not
	// leave a goroutine sending typing actions forever.
	settled := len(api.calls("sendChatAction"))
	time.Sleep(10 * typingRefresh)
	if after := len(api.calls("sendChatAction")); after > settled+1 {
		t.Errorf("the typing indicator kept being sent after the turn ended: %d then %d", settled, after)
	}
}
