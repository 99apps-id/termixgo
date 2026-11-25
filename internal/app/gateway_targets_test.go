package app

import (
	"reflect"
	"testing"

	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/whatsapp"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func TestGatewayTargetsArePrivateAdminsOnly(t *testing.T) {
	cfg := config.Default()
	cfg.Channels.Telegram.Accounts = map[string]config.TelegramAccount{
		"main":   {ChannelAccount: config.ChannelAccount{Enabled: true, Admins: []string{"123", "123", "-10012", "@someone"}, AllowFrom: []string{"456"}}, ChatID: -10090, OwnerUserID: 123},
		"legacy": {ChannelAccount: config.ChannelAccount{Enabled: true}, ChatID: 789},
		"off":    {ChannelAccount: config.ChannelAccount{Enabled: false}, ChatID: 999},
	}
	cfg.Channels.WhatsApp.Accounts = map[string]config.ChannelAccount{
		"wa": {Enabled: true, Admins: []string{"+62812345", "62812345@s.whatsapp.net", "12345@g.us", "name12345", "12345@lid"}, AllowFrom: []string{"62855555"}},
	}
	got := make(map[gatewayAdminTarget]bool)
	for _, target := range gatewayAdminTargets(cfg) {
		got[target] = true
	}
	want := map[gatewayAdminTarget]bool{
		{channel: config.ChannelTelegram, account: "legacy", user: "789"}:                 true,
		{channel: config.ChannelTelegram, account: "main", user: "123"}:                   true,
		{channel: config.ChannelWhatsApp, account: "wa", user: "62812345@s.whatsapp.net"}: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %#v, want %#v", got, want)
	}
}

func TestGatewayTargetsIncludeLinkedWhatsAppOwner(t *testing.T) {
	a := newTestApp(t)
	defer a.Shutdown()
	self := types.JID{User: "62812345", Server: types.DefaultUserServer, Device: 5}
	bot := whatsapp.New(&whatsmeow.Client{Store: &store.Device{ID: &self}}, nil)
	key := channel.AccountID{Channel: config.ChannelWhatsApp, Account: "wa"}
	a.mu.Lock()
	a.cfg.Channels.WhatsApp.Accounts = map[string]config.ChannelAccount{"wa": {Enabled: true}}
	a.channels[key] = &channelRuntime{id: key, waBot: bot}
	a.mu.Unlock()
	targets := a.gatewayTargets(a.Config())
	want := gatewayAdminTarget{channel: config.ChannelWhatsApp, account: "wa", user: "62812345@s.whatsapp.net"}
	for _, target := range targets {
		if target == want {
			return
		}
	}
	t.Fatalf("linked owner missing from targets: %#v", targets)
}
