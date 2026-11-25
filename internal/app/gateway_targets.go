package app

import (
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/types"

	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/whatsapp"
)

// gatewayAdminTarget is deliberately limited to one private account/user.
// Groups, channels, arbitrary guest allowlists, and usernames are not targets.
type gatewayAdminTarget struct {
	channel string
	account string
	user    string
}

// gatewayTargets includes the linked WhatsApp account's own private JID: that
// account is already treated as an operator by the WhatsApp access policy.
func (a *App) gatewayTargets(cfg config.Config) []gatewayAdminTarget {
	targets := gatewayAdminTargets(cfg)
	seen := make(map[gatewayAdminTarget]bool, len(targets))
	for _, target := range targets {
		seen[target] = true
	}
	for _, name := range cfg.EnabledWhatsAppAccounts() {
		key := channel.AccountID{Channel: config.ChannelWhatsApp, Account: name}
		a.mu.Lock()
		runtime := a.channels[key]
		a.mu.Unlock()
		if runtime == nil || runtime.waBot == nil {
			continue
		}
		jid, err := types.ParseJID(runtime.waBot.SelfID())
		if err != nil {
			continue
		}
		id, ok := privateWhatsAppAdmin(jid.ToNonAD().String())
		if !ok {
			continue
		}
		target := gatewayAdminTarget{channel: config.ChannelWhatsApp, account: name, user: id}
		if !seen[target] {
			targets = append(targets, target)
			seen[target] = true
		}
	}
	return targets
}

func gatewayAdminTargets(cfg config.Config) []gatewayAdminTarget {
	var targets []gatewayAdminTarget
	seen := make(map[gatewayAdminTarget]bool)
	add := func(target gatewayAdminTarget) {
		if !seen[target] {
			seen[target] = true
			targets = append(targets, target)
		}
	}
	for _, name := range cfg.EnabledTelegramAccounts() {
		account, ok := cfg.TelegramAccount(name)
		if !ok {
			continue
		}
		ids := append([]string(nil), account.Admins...)
		if account.OwnerUserID > 0 {
			ids = append(ids, strconv.FormatInt(account.OwnerUserID, 10))
		} else if account.ChatID > 0 {
			// A positive Telegram chat ID is a private chat; legacy pairings
			// may not have stored the owner user ID separately.
			ids = append(ids, strconv.FormatInt(account.ChatID, 10))
		}
		for _, raw := range ids {
			id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil || id <= 0 {
				continue
			}
			add(gatewayAdminTarget{channel: config.ChannelTelegram, account: name, user: strconv.FormatInt(id, 10)})
		}
	}
	for _, name := range cfg.EnabledWhatsAppAccounts() {
		account, ok := cfg.WhatsAppAccount(name)
		if !ok {
			continue
		}
		for _, raw := range account.Admins {
			if id, ok := privateWhatsAppAdmin(raw); ok {
				add(gatewayAdminTarget{channel: config.ChannelWhatsApp, account: name, user: id})
			}
		}
	}
	return targets
}

func privateWhatsAppAdmin(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if strings.HasSuffix(raw, "@s.whatsapp.net") {
		raw = strings.TrimSuffix(raw, "@s.whatsapp.net")
	} else if strings.Contains(raw, "@") {
		return "", false
	}
	raw = strings.TrimPrefix(raw, "+")
	if len(raw) < 5 || len(raw) > 20 {
		return "", false
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	// WhatsApp's internal user ids (LIDs) are bare digit strings too, and an
	// operator's own LID is often kept in the admin list so access matching
	// works. They are 15 digits where a real phone number is shorter, and a
	// message to "<lid>@s.whatsapp.net" reaches nobody.
	if len(raw) >= 15 {
		return "", false
	}
	return whatsapp.NormalizeJID(raw), true
}
