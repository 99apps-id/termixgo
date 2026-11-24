package telegram

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The bot token is part of every Bot API path and net/http quotes the URL in a
// transport error, so the error must be cleaned before anything logs it.
func TestTransportErrorsDoNotCarryTheBotToken(t *testing.T) {
	client := newClientAt("123456789:SECRET-TOKEN-VALUE-0123456789abcdef", "http://127.0.0.1:1")
	err := client.SendChatAction(context.Background(), 1, "typing")
	if err == nil {
		t.Fatal("a closed port must fail")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN-VALUE") {
		t.Fatalf("the error carries the token: %v", err)
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Errorf("the underlying error must stay inspectable: %T", err)
	}
	_, dlErr := client.DownloadFile(context.Background(), "photos/a.jpg")
	if dlErr == nil || strings.Contains(dlErr.Error(), "SECRET-TOKEN-VALUE") {
		t.Fatalf("download error = %v", dlErr)
	}
}

func TestPairingLocksAfterRepeatedWrongCodes(t *testing.T) {
	bot := New("123:abc", &recordingAgent{})
	now := time.Now()
	for index := 0; index < pairFailLimit-1; index++ {
		bot.notePairFailure(now)
		if bot.pairingLocked(now) {
			t.Fatalf("locked after only %d wrong codes", index+1)
		}
	}
	bot.notePairFailure(now)
	if !bot.pairingLocked(now) {
		t.Fatalf("%d wrong codes must lock pairing", pairFailLimit)
	}
	if bot.pairingLocked(now.Add(pairLockout + time.Second)) {
		t.Errorf("the lock must end")
	}
}

func TestPairingIgnoresGuessesWhileLocked(t *testing.T) {
	api := newRecordingAPI(t)
	bot := New("123:abc", &recordingAgent{})
	bot.client = api.client()
	bot.SetPairingCode("424242")
	for index := 0; index < pairFailLimit; index++ {
		bot.tryPair(context.Background(), &Message{Chat: Chat{ID: 5, Type: "private"}, From: &User{ID: 5}}, "000000")
	}
	// The correct code is now refused too: the lock does not tell a guess
	// from the answer.
	bot.tryPair(context.Background(), &Message{Chat: Chat{ID: 5, Type: "private"}, From: &User{ID: 5}}, "424242")
	if bot.Paired() {
		t.Fatalf("a locked bot must not pair, even with the right code")
	}
}

func TestPairingWithTheRightCodeStillWorks(t *testing.T) {
	api := newRecordingAPI(t)
	bot := New("123:abc", &recordingAgent{})
	bot.client = api.client()
	bot.SetPairingCode("424242")
	bot.tryPair(context.Background(), &Message{Chat: Chat{ID: 5, Type: "private"}, From: &User{ID: 5}}, "000000")
	bot.tryPair(context.Background(), &Message{Chat: Chat{ID: 5, Type: "private"}, From: &User{ID: 5}}, "424242")
	if !bot.Paired() {
		t.Fatalf("one typo must not stop the operator")
	}
}

// A card posted in a group is visible to everyone there; only the operator may
// press it.
func TestApprovalButtonIsNotOpenToEveryGroupMember(t *testing.T) {
	api := newRecordingAPI(t)
	bot := New("123:abc", &recordingAgent{})
	bot.client = api.client()
	bot.Pair(7, 9)
	decision := make(chan string, 1)
	bot.approvals = map[string]*PendingApproval{"1": {ID: "1", Tool: "run_command", ChatID: -100, Decision: decision}}

	bot.handleCallback(context.Background(), &CallbackQuery{
		ID:      "cb-1",
		Data:    "appr:once:1",
		From:    &User{ID: 555},
		Message: &Message{Chat: Chat{ID: -100, Type: "supergroup"}},
	})
	select {
	case got := <-decision:
		t.Fatalf("a group member approved a tool call: %q", got)
	default:
	}
}

func TestApprovalButtonStillWorksInAPrivateChat(t *testing.T) {
	api := newRecordingAPI(t)
	bot := New("123:abc", &recordingAgent{})
	bot.client = api.client()
	decision := make(chan string, 1)
	bot.approvals = map[string]*PendingApproval{"1": {ID: "1", Tool: "write_file", ChatID: 42, Decision: decision}}

	bot.handleCallback(context.Background(), &CallbackQuery{
		ID:      "cb-1",
		Data:    "appr:once:1",
		From:    &User{ID: 42},
		Message: &Message{Chat: Chat{ID: 42, Type: "private"}},
	})
	select {
	case got := <-decision:
		if got != "once" {
			t.Fatalf("decision = %q", got)
		}
	default:
		t.Fatalf("the person in a private chat could not answer their own approval")
	}
}
