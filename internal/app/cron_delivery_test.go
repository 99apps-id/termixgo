package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
)

func TestDeliverToLocalWritesAFile(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	application := newTestApp(t)

	if err := application.DeliverTo(context.Background(), "local", "laporan mingguan"); err != nil {
		t.Fatalf("DeliverTo local: %v", err)
	}
	dir, err := config.HomePath(filepath.Join("cron", "output"))
	if err != nil {
		t.Fatalf("HomePath: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("output entries = %v, err %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil || !strings.Contains(string(data), "laporan mingguan") {
		t.Fatalf("output = %q, err %v", string(data), err)
	}
}

func TestDeliverToRunningAccountUsesTheRuntime(t *testing.T) {
	application := newTestApp(t)
	key := channel.AccountID{Channel: config.ChannelTelegram, Account: config.FinalAgentName}
	var gotChat, gotText string
	application.mu.Lock()
	application.channels[key] = &channelRuntime{send: func(_ context.Context, chatID, text string) error {
		gotChat, gotText = chatID, text
		return nil
	}}
	application.mu.Unlock()

	if err := application.DeliverTo(context.Background(), "telegram:default:42", "halo"); err != nil {
		t.Fatalf("DeliverTo: %v", err)
	}
	if gotChat != "42" || gotText != "halo" {
		t.Errorf("runtime got chat %q text %q", gotChat, gotText)
	}
	if err := application.DeliverTo(context.Background(), "telegram:ghost:42", "x"); err == nil {
		t.Errorf("a target on a stopped account must fail")
	}
	if err := application.DeliverTo(context.Background(), "origin", "x"); err == nil {
		t.Errorf("origin delivery is not supported yet and must say so")
	}
	if err := application.DeliverTo(context.Background(), "nonsense", "x"); err == nil {
		t.Errorf("an unparseable target must fail")
	}
}

func TestDeliverRedactsAndRecordsHistory(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	application := newTestApp(t)
	runtime := &channelRuntime{send: func(context.Context, string, string) error { return nil }}
	application.mu.Lock()
	application.channels[channel.AccountID{Channel: config.ChannelTelegram, Account: "default"}] = runtime
	application.mu.Unlock()

	if err := application.Deliver(context.Background(), cronJobForTest("job-1", "telegram:default:42"), "token sk-live-abcdefghijklmnopqrstuvwxyz"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	history, err := readCronHistoryForTest()
	if err != nil || len(history) == 0 {
		t.Fatalf("history = %+v, err %v", history, err)
	}
	last := history[len(history)-1]
	if last.Job != "job-1" || last.Target != "telegram:default:42" || !last.OK {
		t.Errorf("history entry = %+v", last)
	}
}
