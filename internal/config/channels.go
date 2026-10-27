package config

import (
	"sort"
	"strings"
)

// Channel names.
const (
	ChannelTelegram = "telegram"
	ChannelWhatsApp = "whatsapp"
)

// Direct-message policy values. A DM that is not allowed is ignored rather
// than answered, so a stranger learns nothing by probing.
const (
	DMPolicyAllowlist = "allowlist"
	DMPolicyOpen      = "open"
	DMPolicyDeny      = "deny"
	// DMPolicyPairing sends an unknown sender a short code and waits for the
	// operator to approve it from a terminal.
	DMPolicyPairing = "pairing"
)

// Group policy values.
const (
	GroupPolicyMention  = "mention"
	GroupPolicyAll      = "all"
	GroupPolicyDisabled = "disabled"
)

// DefaultTextChunkLimit is how long one outbound message may be, matching the
// reference agents. A transport may lower it.
const DefaultTextChunkLimit = 4000

// GuestLimits bounds a non-operator conversation: the steps one turn may take,
// the dollars one chat may spend, and how fast one sender may post.
type GuestLimits struct {
	MaxSteps      int     `json:"maxSteps,omitempty"`
	CostBudgetUSD float64 `json:"costBudgetUsd,omitempty"`
	RatePerMinute int     `json:"ratePerMinute,omitempty"`
}

// Defaults for a guest turn. A zero value in the config means "use the
// default"; a negative value means "no limit".
const (
	DefaultGuestMaxSteps      = 25
	DefaultGuestCostBudgetUSD = 0.5
	DefaultGuestRatePerMinute = 6
)

// Effective fills the guest defaults, keeping an explicit negative value as
// "no limit".
func (g GuestLimits) Effective() GuestLimits {
	out := g
	if out.MaxSteps == 0 {
		out.MaxSteps = DefaultGuestMaxSteps
	}
	if out.CostBudgetUSD == 0 {
		out.CostBudgetUSD = DefaultGuestCostBudgetUSD
	}
	if out.RatePerMinute == 0 {
		out.RatePerMinute = DefaultGuestRatePerMinute
	}
	return out
}

// ChatOverride customizes one conversation, which is how a beta customer or a
// support group gets its own context without a second account.
type ChatOverride struct {
	SystemPrompt string `json:"systemPrompt,omitempty"`
}

// ChannelAccount is the access policy of one account on one channel.
type ChannelAccount struct {
	// Agent names the persona this account speaks as. Empty means the account
	// name itself, which is the common one-account-per-agent setup.
	Agent   string `json:"agent,omitempty"`
	Enabled bool   `json:"enabled,omitempty"`
	// Admins may always talk to the account and are the approval targets.
	Admins []string `json:"admins,omitempty"`
	// DMPolicy is allowlist (default), open, deny or pairing.
	DMPolicy string `json:"dmPolicy,omitempty"`
	// AllowFrom is the sender allowlist a DM policy of allowlist reads.
	AllowFrom []string `json:"allowFrom,omitempty"`
	// GroupPolicy is mention (default), all or disabled.
	GroupPolicy string `json:"groupPolicy,omitempty"`
	// GroupAllowFrom restricts which groups are answered; empty means every
	// group the account is a member of.
	GroupAllowFrom []string `json:"groupAllowFrom,omitempty"`
	// TextChunkLimit is the longest outbound message. Zero means the default.
	TextChunkLimit int `json:"textChunkLimit,omitempty"`
	// ShowProgress sends tool and progress lines; it is meant for admin chats
	// only, so a guest never reads backend noise.
	ShowProgress bool `json:"showProgress,omitempty"`
	// Guest bounds a non-operator conversation. Nil means the defaults.
	Guest *GuestLimits            `json:"guest,omitempty"`
	Chats map[string]ChatOverride `json:"chats,omitempty"`
}

// EffectiveGuestLimits fills the guest defaults when the account carries none.
func (a ChannelAccount) EffectiveGuestLimits() GuestLimits {
	if a.Guest == nil {
		return GuestLimits{}.Effective()
	}
	return a.Guest.Effective()
}

// SetGuestLimits stores guest limits on a copy of the account.
func (a ChannelAccount) SetGuestLimits(limits GuestLimits) ChannelAccount {
	stored := limits
	a.Guest = &stored
	return a
}

// AgentName resolves which agent serves this account.
func (a ChannelAccount) AgentName(account string) string {
	if trimmed := strings.TrimSpace(a.Agent); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(account)
}

// EffectiveTextChunkLimit is the outbound message size this account uses.
func (a ChannelAccount) EffectiveTextChunkLimit() int {
	if a.TextChunkLimit <= 0 {
		return DefaultTextChunkLimit
	}
	return a.TextChunkLimit
}

// TelegramAccount is one Telegram bot: the shared policy plus its pairing
// state. A bot is paired once, from the chat that becomes its operator chat.
type TelegramAccount struct {
	ChannelAccount
	ChatID      int64  `json:"chatId,omitempty"`
	OwnerUserID int64  `json:"ownerUserId,omitempty"`
	PairingCode string `json:"pairingCode,omitempty"`
	BotName     string `json:"botName,omitempty"`
}

// TelegramChannel and WhatsAppChannel are the per-channel account registries.
type TelegramChannel struct {
	Accounts map[string]TelegramAccount `json:"accounts,omitempty"`
}

type WhatsAppChannel struct {
	Accounts map[string]ChannelAccount `json:"accounts,omitempty"`
}

// Channels is every messaging account of this install.
type Channels struct {
	Telegram TelegramChannel `json:"telegram,omitempty"`
	WhatsApp WhatsAppChannel `json:"whatsapp,omitempty"`
}

// NormalizeDMPolicy folds a hand-edited value onto the accepted set, with the
// safe default for anything unknown.
func NormalizeDMPolicy(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case DMPolicyOpen:
		return DMPolicyOpen
	case DMPolicyDeny:
		return DMPolicyDeny
	case DMPolicyPairing:
		return DMPolicyPairing
	default:
		return DMPolicyAllowlist
	}
}

// NormalizeGroupPolicy folds a hand-edited value onto the accepted set, with
// the safe default for anything unknown.
func NormalizeGroupPolicy(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case GroupPolicyAll:
		return GroupPolicyAll
	case GroupPolicyDisabled:
		return GroupPolicyDisabled
	default:
		return GroupPolicyMention
	}
}

// TelegramAccount returns one Telegram account entry.
func (c Config) TelegramAccount(name string) (TelegramAccount, bool) {
	entry, ok := c.Channels.Telegram.Accounts[strings.TrimSpace(name)]
	return entry, ok
}

// WhatsAppAccount returns one WhatsApp account entry.
func (c Config) WhatsAppAccount(name string) (ChannelAccount, bool) {
	entry, ok := c.Channels.WhatsApp.Accounts[strings.TrimSpace(name)]
	return entry, ok
}

// EnabledTelegramAccounts lists the Telegram accounts that should run, sorted.
func (c Config) EnabledTelegramAccounts() []string {
	return sortedEnabledAccounts(len(c.Channels.Telegram.Accounts), func(yield func(string, bool)) {
		for name, entry := range c.Channels.Telegram.Accounts {
			yield(name, entry.Enabled)
		}
	})
}

// EnabledWhatsAppAccounts lists the WhatsApp accounts that should run, sorted.
func (c Config) EnabledWhatsAppAccounts() []string {
	return sortedEnabledAccounts(len(c.Channels.WhatsApp.Accounts), func(yield func(string, bool)) {
		for name, entry := range c.Channels.WhatsApp.Accounts {
			yield(name, entry.Enabled)
		}
	})
}

func sortedEnabledAccounts(size int, collect func(yield func(string, bool))) []string {
	names := make([]string, 0, size)
	collect(func(name string, enabled bool) {
		if enabled {
			names = append(names, name)
		}
	})
	sort.Strings(names)
	return names
}

// WithTelegramAccount sets one Telegram account entry.
func (c Config) WithTelegramAccount(name string, account TelegramAccount) Config {
	if !ValidAgentName(strings.TrimSpace(name)) {
		return c
	}
	next := make(map[string]TelegramAccount, len(c.Channels.Telegram.Accounts)+1)
	for key, value := range c.Channels.Telegram.Accounts {
		next[key] = value
	}
	next[strings.TrimSpace(name)] = account
	c.Channels.Telegram.Accounts = next
	return c
}

// WithWhatsAppAccount sets one WhatsApp account entry.
func (c Config) WithWhatsAppAccount(name string, account ChannelAccount) Config {
	if !ValidAgentName(strings.TrimSpace(name)) {
		return c
	}
	next := make(map[string]ChannelAccount, len(c.Channels.WhatsApp.Accounts)+1)
	for key, value := range c.Channels.WhatsApp.Accounts {
		next[key] = value
	}
	next[strings.TrimSpace(name)] = account
	c.Channels.WhatsApp.Accounts = next
	return c
}

// WithoutChannelAccount removes one account of one channel, reporting whether
// it existed.
func (c Config) WithoutChannelAccount(channel, name string) (Config, bool) {
	trimmed := strings.TrimSpace(name)
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case ChannelTelegram:
		if _, ok := c.Channels.Telegram.Accounts[trimmed]; !ok {
			return c, false
		}
		next := make(map[string]TelegramAccount, len(c.Channels.Telegram.Accounts))
		for key, value := range c.Channels.Telegram.Accounts {
			if key == trimmed {
				continue
			}
			next[key] = value
		}
		c.Channels.Telegram.Accounts = next
		return c, true
	case ChannelWhatsApp:
		if _, ok := c.Channels.WhatsApp.Accounts[trimmed]; !ok {
			return c, false
		}
		next := make(map[string]ChannelAccount, len(c.Channels.WhatsApp.Accounts))
		for key, value := range c.Channels.WhatsApp.Accounts {
			if key == trimmed {
				continue
			}
			next[key] = value
		}
		c.Channels.WhatsApp.Accounts = next
		return c, true
	}
	return c, false
}

// normaliseChannels repairs the channel registry a hand-edited file could
// break, and folds the pre-registry Telegram block into an account entry.
func (c *Config) normaliseChannels() {
	telegram := make(map[string]TelegramAccount, len(c.Channels.Telegram.Accounts)+1)
	for name, entry := range c.Channels.Telegram.Accounts {
		trimmed := strings.TrimSpace(name)
		if !ValidAgentName(trimmed) {
			continue
		}
		entry.ChannelAccount = normaliseChannelAccount(entry.ChannelAccount)
		entry.PairingCode = strings.TrimSpace(entry.PairingCode)
		entry.BotName = strings.TrimSpace(entry.BotName)
		telegram[trimmed] = entry
	}
	whatsapp := make(map[string]ChannelAccount, len(c.Channels.WhatsApp.Accounts))
	for name, entry := range c.Channels.WhatsApp.Accounts {
		trimmed := strings.TrimSpace(name)
		if !ValidAgentName(trimmed) {
			continue
		}
		whatsapp[trimmed] = normaliseChannelAccount(entry)
	}
	c.Channels.Telegram.Accounts = telegram
	c.Channels.WhatsApp.Accounts = whatsapp

	legacy := c.Telegram
	migrated := legacy.Enabled || legacy.ChatID != 0 || legacy.OwnerUserID != 0 || strings.TrimSpace(legacy.PairingCode) != ""
	if migrated {
		if _, exists := c.Channels.Telegram.Accounts[FinalAgentName]; !exists {
			c.Channels.Telegram.Accounts[FinalAgentName] = TelegramAccount{
				ChannelAccount: ChannelAccount{
					Agent:   FinalAgentName,
					Enabled: legacy.Enabled,
				},
				ChatID:      legacy.ChatID,
				OwnerUserID: legacy.OwnerUserID,
				PairingCode: strings.TrimSpace(legacy.PairingCode),
			}
		}
	}
	// Bridge: the pre-registry block mirrors the default account until the
	// channel runtime replaces the old one, so both readers see one truth.
	if entry, ok := c.Channels.Telegram.Accounts[FinalAgentName]; ok {
		c.Telegram = Telegram{
			Enabled:     entry.Enabled,
			ChatID:      entry.ChatID,
			OwnerUserID: entry.OwnerUserID,
			PairingCode: entry.PairingCode,
		}
	}
}

// SyncLegacyTelegram writes the pairing state to both shapes: the default
// account the channel runtime reads, and the pre-registry block the old
// runtime still writes. The two are one fact with two readers.
func (c *Config) SyncLegacyTelegram(enabled bool, chatID, ownerUserID int64, pairingCode string) {
	pairingCode = strings.TrimSpace(pairingCode)
	c.Telegram = Telegram{Enabled: enabled, ChatID: chatID, OwnerUserID: ownerUserID, PairingCode: pairingCode}

	entry := c.Channels.Telegram.Accounts[FinalAgentName]
	if strings.TrimSpace(entry.Agent) == "" {
		entry.Agent = FinalAgentName
	}
	entry.Enabled = enabled
	entry.ChatID = chatID
	entry.OwnerUserID = ownerUserID
	entry.PairingCode = pairingCode
	next := make(map[string]TelegramAccount, len(c.Channels.Telegram.Accounts)+1)
	for key, value := range c.Channels.Telegram.Accounts {
		next[key] = value
	}
	next[FinalAgentName] = entry
	c.Channels.Telegram.Accounts = next
}

func normaliseChannelAccount(entry ChannelAccount) ChannelAccount {
	entry.Agent = strings.TrimSpace(entry.Agent)
	entry.Admins = trimList(entry.Admins)
	entry.AllowFrom = trimList(entry.AllowFrom)
	entry.GroupAllowFrom = trimList(entry.GroupAllowFrom)
	entry.DMPolicy = NormalizeDMPolicy(entry.DMPolicy)
	entry.GroupPolicy = NormalizeGroupPolicy(entry.GroupPolicy)
	if entry.TextChunkLimit > 20_000 {
		entry.TextChunkLimit = 20_000
	}
	if entry.Guest != nil {
		limits := *entry.Guest
		if limits.MaxSteps < 0 {
			limits.MaxSteps = -1
		}
		if limits.CostBudgetUSD < 0 {
			limits.CostBudgetUSD = -1
		}
		if limits.RatePerMinute < 0 {
			limits.RatePerMinute = -1
		}
		if limits.MaxSteps == 0 && limits.CostBudgetUSD == 0 && limits.RatePerMinute == 0 {
			// An empty block configures nothing; keeping it out of the file is
			// what stops every account listing from carrying a blank form.
			entry.Guest = nil
		} else {
			entry.Guest = &limits
		}
	}
	if entry.Chats != nil {
		chats := make(map[string]ChatOverride, len(entry.Chats))
		for id, override := range entry.Chats {
			trimmed := strings.TrimSpace(id)
			if trimmed == "" {
				continue
			}
			override.SystemPrompt = strings.TrimSpace(override.SystemPrompt)
			chats[trimmed] = override
		}
		entry.Chats = chats
	}
	return entry
}

// trimList drops empty entries and trims the rest, keeping order.
func trimList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
