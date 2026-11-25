package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/channel"
)

func TestDurableInboxRecoveryAndDeduplication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.sqlite")
	inbox, err := openDurableInbox(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := inbox.accept([]Update{{UpdateID: 10}, {UpdateID: 12}}); err != nil {
		t.Fatal(err)
	}
	if got, err := inbox.offset(); err != nil || got != 13 {
		t.Fatalf("offset=%d err=%v", got, err)
	}
	if err := inbox.accept([]Update{{UpdateID: 10}, {UpdateID: 12}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := inbox.claim(10); err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	if err := inbox.close(); err != nil {
		t.Fatal(err)
	}
	inbox, err = openDurableInbox(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.close()
	if got, err := inbox.offset(); err != nil || got != 13 {
		t.Fatalf("restart offset=%d err=%v", got, err)
	}
	pending, err := inbox.queued()
	if err != nil || len(pending) != 1 || pending[0].UpdateID != 12 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	if count, err := inbox.uncertainCount(); err != nil || count != 1 {
		t.Fatalf("uncertain=%d err=%v", count, err)
	}
	// Windows does not report unix permission bits; the file is restricted by ACL there.
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode=%v err=%v", info, err)
		}
	}
}

func TestDurableInboxSerializesMessagesInSameChat(t *testing.T) {
	api := newRecordingAPI(t)
	agent := newBlockingAgent()
	bot := New("123:abc", agent)
	bot.client = api.client()
	bot.Pair(7, 9)
	inbox, err := openDurableInbox(filepath.Join(t.TempDir(), "inbox.sqlite"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.close()
	defer func() {
		select {
		case <-agent.release:
		default:
			close(agent.release)
		}
		bot.handlers.Wait()
	}()
	update := func(id int64, text string) Update {
		return Update{UpdateID: id, Message: &Message{Chat: Chat{ID: 7, Type: "private"}, From: &User{ID: 9}, Text: text}}
	}
	if err := inbox.accept([]Update{update(1, "one")}); err != nil {
		t.Fatal(err)
	}
	if err := bot.dispatchQueued(context.Background(), inbox); err != nil {
		t.Fatal(err)
	}
	select {
	case <-agent.started:
	case <-time.After(3 * time.Second):
		t.Fatal("first prompt did not start")
	}
	if err := inbox.accept([]Update{update(2, "two")}); err != nil {
		t.Fatal(err)
	}
	if err := bot.dispatchQueued(context.Background(), inbox); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	var state string
	if err := inbox.db.QueryRow(`SELECT state FROM updates WHERE id=2`).Scan(&state); err != nil {
		t.Fatalf("second update lost: %v", err)
	}
	if state != "queued" {
		t.Fatalf("second state=%q, want queued", state)
	}
	close(agent.release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if agent.runCount() == 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("queued second prompt did not run: %d", agent.runCount())
}

func TestDurableInboxFailureDoesNotAdvanceOffset(t *testing.T) {
	inbox, err := openDurableInbox(filepath.Join(t.TempDir(), "inbox.sqlite"), 10)
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.close()
	_, err = inbox.db.Exec(`CREATE TRIGGER reject_test BEFORE INSERT ON updates WHEN NEW.id=12 BEGIN SELECT RAISE(ABORT, 'simulated failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err := inbox.accept([]Update{{UpdateID: 10}, {UpdateID: 12}}); err == nil {
		t.Fatal("expected failed transaction")
	}
	if got, err := inbox.offset(); err != nil || got != 10 {
		t.Fatalf("offset=%d err=%v", got, err)
	}
	pending, err := inbox.queued()
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
}

func TestDurableInboxPreservesRawUpdateAndResetsIdleSequence(t *testing.T) {
	inbox, err := openDurableInbox(filepath.Join(t.TempDir(), "inbox.sqlite"), 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.close()
	now := time.Unix(1800000000, 0)
	if got, err := inbox.pollOffset(now); err != nil || got != 0 {
		t.Fatalf("initial poll offset=%d err=%v", got, err)
	}
	raw := []byte(`{"update_id":12,"future_telegram_field":{"x":true}}`)
	var update Update
	if err := json.Unmarshal(raw, &update); err != nil {
		t.Fatal(err)
	}
	rebased, err := inbox.acceptPolled([]Update{update}, 0, now)
	if err != nil || !rebased {
		t.Fatalf("rebase=%v err=%v", rebased, err)
	}
	if got, err := inbox.offset(); err != nil || got != 13 {
		t.Fatalf("offset=%d err=%v", got, err)
	}
	if got, err := inbox.uncertainCount(); err != nil || got != 1 {
		t.Fatalf("uncertain=%d err=%v", got, err)
	}
	var saved []byte
	if err := inbox.db.QueryRow(`SELECT payload FROM updates WHERE id=12`).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, raw) {
		t.Fatalf("raw payload was not preserved: %s", saved)
	}
	if got, err := inbox.pollOffset(now.Add(5 * 24 * time.Hour)); err != nil || got != 13 {
		t.Fatalf("active offset=%d err=%v", got, err)
	}
	if got, err := inbox.pollOffset(now.Add(6 * 24 * time.Hour)); err != nil || got != 0 {
		t.Fatalf("idle offset=%d err=%v", got, err)
	}
}

func TestDurableInboxMarksPanickedHandlerUncertain(t *testing.T) {
	inbox, err := openDurableInbox(filepath.Join(t.TempDir(), "inbox.sqlite"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.close()
	bot := New("123:abc", nil)
	bot.Pair(7, 9)
	update := Update{UpdateID: 1, Message: &Message{Chat: Chat{ID: 7, Type: "private"}, From: &User{ID: 9}, Text: "/status"}}
	if err := inbox.accept([]Update{update}); err != nil {
		t.Fatal(err)
	}
	if err := bot.dispatchQueued(context.Background(), inbox); err != nil {
		t.Fatal(err)
	}
	bot.handlers.Wait()
	if got, err := inbox.uncertainCount(); err != nil || got != 1 {
		t.Fatalf("uncertain=%d err=%v", got, err)
	}
}

// steeringAgent reports a run in flight and records the steer it is handed.
type steeringAgent struct {
	*blockingAgent
	mu     sync.Mutex
	steers []string
}

func (a *steeringAgent) IsRunning(channel.Request) bool { return true }

func (a *steeringAgent) Steer(_ channel.Request, text string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steers = append(a.steers, text)
	return true
}

// A message sent while a turn runs reaches the live run as a steer. The
// durable queue must not hold it back until the turn is over.
func TestDurableInboxLetsASteerReachARunningTurn(t *testing.T) {
	api := newRecordingAPI(t)
	agent := &steeringAgent{blockingAgent: newBlockingAgent()}
	bot := New("123:abc", agent)
	bot.client = api.client()
	bot.Pair(7, 9)
	inbox, err := openDurableInbox(filepath.Join(t.TempDir(), "inbox.sqlite"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.close()
	defer func() {
		select {
		case <-agent.release:
		default:
			close(agent.release)
		}
		bot.handlers.Wait()
	}()
	update := func(id int64, text string) Update {
		return Update{UpdateID: id, Message: &Message{Chat: Chat{ID: 7, Type: "private"}, From: &User{ID: 9}, Text: text}}
	}
	// The first message holds the chat, so it runs; the second must not wait.
	if _, ok := bot.serialInboxChat(update(1, "one")); ok {
		t.Fatalf("a message during a running turn must not be held in the chat queue")
	}
	if err := inbox.accept([]Update{update(2, "add a table")}); err != nil {
		t.Fatal(err)
	}
	if err := bot.dispatchQueued(context.Background(), inbox); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		agent.mu.Lock()
		got := append([]string(nil), agent.steers...)
		agent.mu.Unlock()
		if len(got) == 1 && strings.Contains(got[0], "add a table") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the steer never reached the running turn")
}
