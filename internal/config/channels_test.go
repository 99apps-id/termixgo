package config

import (
	"testing"
)

func TestNormaliseChannelsMigratesLegacyTelegram(t *testing.T) {
	cfg := Default()
	cfg.Telegram = Telegram{Enabled: true, ChatID: 42, OwnerUserID: 7, PairingCode: "123456"}
	cfg.normalise()

	entry, ok := cfg.TelegramAccount(FinalAgentName)
	if !ok {
		t.Fatalf("the legacy pairing did not become the default account")
	}
	if !entry.Enabled || entry.ChatID != 42 || entry.OwnerUserID != 7 || entry.PairingCode != "123456" {
		t.Errorf("migrated entry = %+v", entry)
	}
	if entry.AgentName(FinalAgentName) != FinalAgentName {
		t.Errorf("an account with no agent must speak as the account name")
	}
	// The legacy block mirrors the account so the pre-registry runtime keeps
	// working until the channel runtime replaces it.
	if cfg.Telegram.ChatID != 42 || !cfg.Telegram.Enabled {
		t.Errorf("legacy block lost the pairing: %+v", cfg.Telegram)
	}
}

func TestNormaliseChannelsAccountWinsOverLegacy(t *testing.T) {
	cfg := Default()
	cfg.Channels.Telegram.Accounts = map[string]TelegramAccount{
		FinalAgentName: {ChannelAccount: ChannelAccount{Enabled: true}, ChatID: 9},
	}
	cfg.Telegram = Telegram{Enabled: false, ChatID: 1}
	cfg.normalise()

	if cfg.Telegram.ChatID != 9 || !cfg.Telegram.Enabled {
		t.Errorf("the account must be authoritative: %+v", cfg.Telegram)
	}
}

func TestNormaliseChannelAccountsRepairsPolicy(t *testing.T) {
	cfg := Default()
	cfg.Channels.WhatsApp.Accounts = map[string]ChannelAccount{
		"sales":     {DMPolicy: "nonsense", GroupPolicy: "nonsense", AllowFrom: []string{" 6281 ", ""}, TextChunkLimit: 999999},
		"Bad Name!": {},
	}
	cfg.normalise()

	entry, ok := cfg.WhatsAppAccount("sales")
	if !ok {
		t.Fatalf("the sales account was dropped")
	}
	if entry.DMPolicy != DMPolicyAllowlist || entry.GroupPolicy != GroupPolicyMention {
		t.Errorf("policies were not repaired: %+v", entry)
	}
	if len(entry.AllowFrom) != 1 || entry.AllowFrom[0] != "6281" {
		t.Errorf("allowlist was not trimmed: %+v", entry.AllowFrom)
	}
	if entry.TextChunkLimit != 20000 {
		t.Errorf("textChunkLimit = %d, want the clamp", entry.TextChunkLimit)
	}
	if entry.AgentName("sales") != "sales" {
		t.Errorf("an account name must double as the agent name")
	}
	if _, ok := cfg.WhatsAppAccount("Bad Name!"); ok {
		t.Errorf("an invalid account name survived normalisation")
	}
}

func TestEffectiveLimits(t *testing.T) {
	limits := GuestLimits{}.Effective()
	if limits.MaxSteps != DefaultGuestMaxSteps || limits.CostBudgetUSD != DefaultGuestCostBudgetUSD || limits.RatePerMinute != DefaultGuestRatePerMinute {
		t.Errorf("guest defaults = %+v", limits)
	}
	unlimited := GuestLimits{MaxSteps: -1}.Effective()
	if unlimited.MaxSteps != -1 {
		t.Errorf("a negative step limit means no limit, got %d", unlimited.MaxSteps)
	}

	if got := (ChannelAccount{}).EffectiveTextChunkLimit(); got != DefaultTextChunkLimit {
		t.Errorf("text chunk default = %d", got)
	}
	if got := (ChannelAccount{TextChunkLimit: 100}).EffectiveTextChunkLimit(); got != 100 {
		t.Errorf("configured chunk limit = %d", got)
	}
}

func TestChannelAccountRegistry(t *testing.T) {
	cfg := Default()
	updated := cfg.WithWhatsAppAccount("sales", ChannelAccount{Enabled: true})
	if _, ok := cfg.WhatsAppAccount("sales"); ok {
		t.Errorf("WithWhatsAppAccount mutated the receiver")
	}
	if _, ok := updated.WhatsAppAccount("sales"); !ok {
		t.Fatalf("the account was not added")
	}
	removed, ok := updated.WithoutChannelAccount(ChannelWhatsApp, "sales")
	if !ok {
		t.Fatalf("WithoutChannelAccount reported nothing removed")
	}
	if _, ok := removed.WhatsAppAccount("sales"); ok {
		t.Errorf("the account survived removal")
	}
	if updated.WithWhatsAppAccount("Bad Name!", ChannelAccount{}).Channels.WhatsApp.Accounts["Bad Name!"].Enabled {
		t.Errorf("an invalid name was accepted")
	}
}

func TestEnabledAccountsAreSorted(t *testing.T) {
	cfg := Default()
	cfg = cfg.WithTelegramAccount("zeta", TelegramAccount{ChannelAccount: ChannelAccount{Enabled: true}})
	cfg = cfg.WithTelegramAccount("alpha", TelegramAccount{ChannelAccount: ChannelAccount{Enabled: true}})
	cfg = cfg.WithTelegramAccount("off", TelegramAccount{ChannelAccount: ChannelAccount{Enabled: false}})

	names := cfg.EnabledTelegramAccounts()
	if len(names) != 2 || names[0] != "alpha" || names[1] != "zeta" {
		t.Errorf("EnabledTelegramAccounts = %v", names)
	}
}

func TestSyncLegacyTelegramWritesBothShapes(t *testing.T) {
	cfg := Default()
	cfg.SyncLegacyTelegram(true, 77, 5, "")
	if cfg.Telegram.ChatID != 77 || !cfg.Telegram.Enabled || cfg.Telegram.OwnerUserID != 5 {
		t.Errorf("legacy block not written: %+v", cfg.Telegram)
	}
	entry, ok := cfg.TelegramAccount(FinalAgentName)
	if !ok || entry.ChatID != 77 || !entry.Enabled {
		t.Errorf("default account not written: %+v", entry)
	}
}
