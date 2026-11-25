package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
)

func TestGatewayDisconnectAlertsAdminsAndRetriesOfflineChannel(t *testing.T) {
	a := newTestApp(t)
	defer a.Shutdown()
	tgKey := channel.AccountID{Channel: config.ChannelTelegram, Account: "main"}
	waKey := channel.AccountID{Channel: config.ChannelWhatsApp, Account: "wa"}
	var telegramRecipients, whatsappRecipients []string
	waOnline := false
	a.mu.Lock()
	a.cfg.Channels.Telegram.Accounts = map[string]config.TelegramAccount{
		"main": {ChannelAccount: config.ChannelAccount{Enabled: true, AllowFrom: []string{"999"}}, OwnerUserID: 123},
	}
	a.cfg.Channels.WhatsApp.Accounts = map[string]config.ChannelAccount{
		"wa": {Enabled: true, Admins: []string{"62812345"}, AllowFrom: []string{"62899999"}},
	}
	a.channels[tgKey] = &channelRuntime{id: tgKey, send: func(_ context.Context, recipient, _ string) error {
		telegramRecipients = append(telegramRecipients, recipient)
		return nil
	}}
	a.channels[waKey] = &channelRuntime{id: waKey, send: func(_ context.Context, recipient, _ string) error {
		if !waOnline {
			return errors.New("offline")
		}
		whatsappRecipients = append(whatsappRecipients, recipient)
		return nil
	}}
	a.mu.Unlock()
	outbox, err := openGatewayOutbox(filepath.Join(t.TempDir(), "gateway.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.db.Close()
	n := &gatewayNotifier{outbox: outbox, ctx: context.Background()}
	n.record(a, waKey, "online")
	n.record(a, waKey, "offline")
	n.flush(a)
	if len(telegramRecipients) != 1 || telegramRecipients[0] != "123" || len(whatsappRecipients) != 0 {
		t.Fatalf("telegram=%v whatsapp=%v", telegramRecipients, whatsappRecipients)
	}
	pending, err := outbox.pending()
	if err != nil || len(pending) != 1 || pending[0].target.user != "62812345@s.whatsapp.net" {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	waOnline = true
	n.flush(a)
	if len(whatsappRecipients) != 1 || whatsappRecipients[0] != "62812345@s.whatsapp.net" {
		t.Fatalf("whatsapp=%v", whatsappRecipients)
	}
}

func TestGatewayNotificationRetriesAndRevalidatesAdmin(t *testing.T) {
	a := newTestApp(t)
	defer a.Shutdown()
	key := channel.AccountID{Channel: config.ChannelTelegram, Account: "main"}
	a.mu.Lock()
	a.cfg.Channels.Telegram.Accounts = map[string]config.TelegramAccount{
		"main": {ChannelAccount: config.ChannelAccount{Enabled: true}, OwnerUserID: 123},
	}
	fail := true
	var recipients []string
	a.channels[key] = &channelRuntime{id: key, send: func(_ context.Context, recipient, text string) error {
		if fail {
			return errors.New("temporarily offline")
		}
		recipients = append(recipients, recipient)
		return nil
	}}
	a.mu.Unlock()
	outbox, err := openGatewayOutbox(filepath.Join(t.TempDir(), "gateway.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.db.Close()
	n := &gatewayNotifier{outbox: outbox, ctx: context.Background()}
	targets := gatewayAdminTargets(a.Config())
	if _, err := outbox.transition("whatsapp/wa", "online", targets); err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.transition("whatsapp/wa", "offline", targets); err != nil {
		t.Fatal(err)
	}
	n.flush(a)
	pending, err := outbox.pending()
	if err != nil || len(pending) != 1 {
		t.Fatalf("offline pending=%v err=%v", pending, err)
	}
	fail = false
	n.flush(a)
	pending, err = outbox.pending()
	if err != nil || len(pending) != 0 || len(recipients) != 1 || recipients[0] != "123" {
		t.Fatalf("delivered=%v pending=%v err=%v", recipients, pending, err)
	}
	if _, err := outbox.transition("whatsapp/wa", "online", targets); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	entry := a.cfg.Channels.Telegram.Accounts["main"]
	entry.OwnerUserID = 456
	a.cfg.Channels.Telegram.Accounts["main"] = entry
	a.mu.Unlock()
	n.flush(a)
	pending, err = outbox.pending()
	if err != nil || len(pending) != 0 || len(recipients) != 1 {
		t.Fatalf("revoked admin received alert: recipients=%v pending=%v err=%v", recipients, pending, err)
	}
}

func TestGatewayFailedTransitionPersistsAndRetriesWhenAdminAppears(t *testing.T) {
	a := newTestApp(t)
	defer a.Shutdown()
	tgKey := channel.AccountID{Channel: config.ChannelTelegram, Account: "main"}
	waKey := channel.AccountID{Channel: config.ChannelWhatsApp, Account: "wa"}
	path := filepath.Join(t.TempDir(), "gateway.sqlite")
	outbox, err := openGatewayOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	n := &gatewayNotifier{outbox: outbox, ctx: context.Background()}
	// Baseline online recorded with no admins yet (initial online needs no targets).
	n.record(a, waKey, "online")
	// Offline occurs while no private admin target exists; transition must be deferred.
	n.record(a, waKey, "offline")
	if err := outbox.db.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate restart and admin configuration becoming available.
	outbox, err = openGatewayOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.db.Close()
	var recipients []string
	var messages []string
	a.mu.Lock()
	a.cfg.Channels.Telegram.Accounts = map[string]config.TelegramAccount{
		"main": {ChannelAccount: config.ChannelAccount{Enabled: true}, OwnerUserID: 123},
	}
	a.channels[tgKey] = &channelRuntime{id: tgKey, send: func(_ context.Context, recipient, text string) error {
		recipients = append(recipients, recipient)
		messages = append(messages, text)
		return nil
	}}
	a.mu.Unlock()

	n = &gatewayNotifier{outbox: outbox, ctx: context.Background()}
	n.flush(a)
	if len(recipients) != 1 || recipients[0] != "123" {
		t.Fatalf("deferred transition was not delivered after restart: recipients=%v messages=%v", recipients, messages)
	}
	remaining, err := outbox.pendingTransitions()
	if err != nil || len(remaining) != 0 {
		t.Fatalf("pending transitions not cleared: remaining=%v err=%v", remaining, err)
	}
}
