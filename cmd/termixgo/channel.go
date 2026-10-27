package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/99apps-id/termixgo/internal/agenthome"
	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/secrets"
	"github.com/99apps-id/termixgo/internal/telegram"
	"github.com/99apps-id/termixgo/internal/whatsapp"
)

// runChannel manages the messaging accounts: their policy, their credentials
// and the pairing requests of unknown senders.
func runChannel(args []string, stdout io.Writer) error {
	if len(args) == 0 || strings.EqualFold(strings.TrimSpace(args[0]), "list") {
		return listChannels(args[1:], stdout)
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "add":
		return addChannel(args[1:], stdout)
	case "login":
		return loginChannel(args[1:], stdout)
	case "on", "off":
		return toggleChannel(strings.ToLower(args[0]), args[1:], stdout)
	case "status":
		return statusChannel(args[1:], stdout)
	case "rm", "remove":
		return removeChannel(args[1:], stdout)
	case "allow", "admin":
		return editChannelList(args[1:], strings.ToLower(args[0]), stdout)
	case "groups":
		return editChannelGroups(args[1:], stdout)
	case "guest":
		return editChannelGuest(args[1:], stdout)
	case "pairing":
		return pairingCommand(args[1:], stdout)
	default:
		return fmt.Errorf("usage: termixgo channel [list|add|login|on|off|status|rm|allow|admin|groups|guest|pairing]")
	}
}

// channelType normalizes the channel name the operator typed.
func channelType(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "whatsapp", "wa":
		return config.ChannelWhatsApp, nil
	case "telegram", "tg":
		return config.ChannelTelegram, nil
	default:
		return "", fmt.Errorf("unknown channel %q; use whatsapp or telegram", raw)
	}
}

func listChannels(args []string, stdout io.Writer) error {
	agentFilter := ""
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--agent":
			if index+1 >= len(args) {
				return fmt.Errorf("--agent needs a name")
			}
			index++
			agentFilter = args[index]
		default:
			return fmt.Errorf("unknown channel list option %q", args[index])
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	printed := false
	type row struct {
		channel string
		name    string
		agent   string
		enabled bool
		state   string
	}
	var rows []row
	for name, entry := range cfg.Channels.Telegram.Accounts {
		rows = append(rows, row{config.ChannelTelegram, name, entry.AgentName(name), entry.Enabled, telegramState(cfg, name, entry)})
	}
	for name, entry := range cfg.Channels.WhatsApp.Accounts {
		rows = append(rows, row{config.ChannelWhatsApp, name, entry.AgentName(name), entry.Enabled, whatsappState(cfg, name)})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].channel != rows[j].channel {
			return rows[i].channel < rows[j].channel
		}
		return rows[i].name < rows[j].name
	})
	for _, item := range rows {
		if agentFilter != "" && item.agent != agentFilter {
			continue
		}
		printed = true
		state := "off"
		if item.enabled {
			state = "on"
		}
		fmt.Fprintf(stdout, "%-9s %-16s %-4s agent=%-12s %s\n", item.channel, item.name, state, item.agent, item.state)
	}
	if !printed {
		fmt.Fprintln(stdout, "No channel accounts configured.")
		fmt.Fprintln(stdout, "Add one with: termixgo channel add whatsapp <agent>")
	}
	return nil
}

func telegramState(cfg config.Config, name string, entry config.TelegramAccount) string {
	switch {
	case !hasTelegramToken(name):
		return "no token (run: channel login telegram " + name + " <token>)"
	case entry.ChatID != 0:
		return fmt.Sprintf("paired chat %d", entry.ChatID)
	case strings.TrimSpace(entry.PairingCode) != "":
		return "waiting for /pair " + entry.PairingCode
	default:
		return "token stored, not paired"
	}
}

// hasTelegramToken reports whether an account's bot token is stored, without
// ever returning the token itself.
func hasTelegramToken(name string) bool {
	store, err := secrets.Load()
	if err != nil {
		return false
	}
	return store.Has(secrets.TelegramAccountTokenKey(name))
}

func whatsappState(cfg config.Config, name string) string {
	agent := cfg.Channels.WhatsApp.Accounts[name].AgentName(name)
	paths, err := agenthome.Resolve(agent, cfg.Agent(agent).Workspace)
	if err != nil {
		return "unknown agent"
	}
	session := whatsapp.SessionPath(paths.Home, name)
	if _, err := os.Stat(session); err == nil {
		return "linked"
	}
	return "not linked"
}

func addChannel(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: termixgo channel add <whatsapp|telegram> [agent]")
	}
	kind, err := channelType(args[0])
	if err != nil {
		return err
	}
	name := config.FinalAgentName
	if len(args) > 1 {
		name = strings.TrimSpace(args[1])
	}
	if len(args) > 2 {
		return fmt.Errorf("usage: termixgo channel add <whatsapp|telegram> [agent]")
	}
	if !config.ValidAgentName(name) {
		return fmt.Errorf("invalid agent name %q", name)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.HasAgent(name) {
		// The plan's one-account-per-agent default: adding an account for an
		// unknown agent creates that agent first.
		paths, err := agenthome.Ensure(name, "", "assistant")
		if err != nil {
			return err
		}
		cfg = cfg.Trust(paths.Workspace)
		cfg = cfg.WithAgent(name, config.Agent{})
		fmt.Fprintf(stdout, "agent %q created (persona: %s)\n", name, paths.Soul)
	}

	switch kind {
	case config.ChannelTelegram:
		if _, ok := cfg.TelegramAccount(name); ok {
			return fmt.Errorf("telegram account %q already exists", name)
		}
		cfg = cfg.WithTelegramAccount(name, config.TelegramAccount{
			ChannelAccount: config.ChannelAccount{Agent: name, DMPolicy: config.DMPolicyAllowlist, GroupPolicy: config.GroupPolicyMention},
		})
	case config.ChannelWhatsApp:
		if _, ok := cfg.WhatsAppAccount(name); ok {
			return fmt.Errorf("whatsapp account %q already exists", name)
		}
		cfg = cfg.WithWhatsAppAccount(name, config.ChannelAccount{
			Agent: name, DMPolicy: config.DMPolicyAllowlist, GroupPolicy: config.GroupPolicyMention,
		})
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s account %q added (disabled until login)\n", kind, name)
	fmt.Fprintf(stdout, "Next: termixgo channel login %s %s\n", kind, name)
	return nil
}

func loginChannel(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: termixgo channel login <whatsapp|telegram> [agent] [--pair <phone>|<token>]")
	}
	kind, err := channelType(args[0])
	if err != nil {
		return err
	}
	name := config.FinalAgentName
	seenName := false
	token := ""
	pairPhone := ""
	for index := 1; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--pair":
			if index+1 >= len(args) {
				return fmt.Errorf("--pair needs a phone number")
			}
			index++
			pairPhone = strings.TrimSpace(args[index])
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("unknown channel login option %q", arg)
		case !seenName:
			name = strings.TrimSpace(arg)
			seenName = true
		default:
			token = strings.TrimSpace(arg)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch kind {
	case config.ChannelWhatsApp:
		return loginWhatsApp(cfg, name, pairPhone, token, stdout)
	case config.ChannelTelegram:
		entry, ok := cfg.TelegramAccount(name)
		if !ok {
			return fmt.Errorf("no telegram account %q; add it with 'termixgo channel add telegram %s'", name, name)
		}
		if token == "" {
			token = strings.TrimSpace(os.Getenv("TERMIXGO_TELEGRAM_TOKEN"))
		}
		if token == "" {
			return fmt.Errorf("usage: termixgo channel login telegram %s <bot-token>", name)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		user, err := telegram.NewClient(token).GetMe(ctx)
		if err != nil {
			return fmt.Errorf("the token was rejected: %w", err)
		}
		store, err := secrets.Load()
		if err != nil {
			return err
		}
		if err := store.Set(secrets.TelegramAccountTokenKey(name), token); err != nil {
			return err
		}
		entry.Enabled = true
		if strings.TrimSpace(entry.Agent) == "" {
			entry.Agent = name
		}
		if strings.TrimSpace(entry.BotName) == "" {
			entry.BotName = user.Username
		}
		cfg = cfg.WithTelegramAccount(name, entry)
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "telegram account %q is enabled for bot @%s\n", name, user.Username)
		if code := strings.TrimSpace(entry.PairingCode); code != "" {
			fmt.Fprintf(stdout, "Pair it from the chat that should be the operator: send /pair %s\n", code)
		} else {
			fmt.Fprintln(stdout, "Pair it from the chat that should be the operator: run 'termixgo setup' or /telegram pair to get a code, then send /pair <code>")
		}
		return nil
	}
	return nil
}

// loginWhatsApp links one device by QR or by pairing code. The session file is
// written under the owning agent's home, and the account is enabled only after
// the phone confirms.
func loginWhatsApp(cfg config.Config, name, pairPhone, token string, stdout io.Writer) error {
	entry, ok := cfg.WhatsAppAccount(name)
	if !ok {
		return fmt.Errorf("no whatsapp account %q; add it with 'termixgo channel add whatsapp %s'", name, name)
	}
	if strings.TrimSpace(token) != "" {
		return fmt.Errorf("whatsapp login does not take a token; use --pair <phone> for a pairing code, or no flag for a QR code")
	}
	agentName := entry.AgentName(name)
	paths, err := agenthome.Resolve(agentName, cfg.Agent(agentName).Workspace)
	if err != nil {
		return err
	}
	device, closeStore, err := whatsapp.OpenDevice(context.Background(), whatsapp.SessionPath(paths.Home, name))
	if err != nil {
		return err
	}
	defer func() { _ = closeStore() }()

	if device.ID != nil {
		entry.Enabled = true
		cfg = cfg.WithWhatsAppAccount(name, entry)
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "whatsapp account %q is already linked as %s (enabled)\n", name, whatsapp.PhoneFromJID(device.ID.String()))
		return nil
	}

	client := whatsapp.Client(device)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if pairPhone != "" {
		code, err := whatsapp.PairWithPhone(ctx, client, pairPhone, "Chrome (Linux)")
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Enter this on your phone: WhatsApp > Linked devices > Link with phone number\n  pairing code: %s\n", code)
		if err := whatsapp.WaitForLogin(ctx, client, 160*time.Second); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(stdout, "Scan this in WhatsApp > Linked devices > Link a device:")
		if err := whatsapp.Login(ctx, client, func(code string) {
			qrterminal.GenerateHalfBlock(code, qrterminal.L, stdout)
			fmt.Fprintln(stdout, "(the code refreshes; scan the newest one)")
		}); err != nil {
			return err
		}
	}

	linked := ""
	if client.Store != nil && client.Store.ID != nil {
		linked = client.Store.ID.String()
	}
	client.Disconnect()
	entry.Enabled = true
	cfg = cfg.WithWhatsAppAccount(name, entry)
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "whatsapp account %q linked as %s and enabled\n", name, whatsapp.PhoneFromJID(linked))
	return nil
}

func toggleChannel(state string, args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: termixgo channel %s <whatsapp|telegram> [agent]", state)
	}
	kind, err := channelType(args[0])
	if err != nil {
		return err
	}
	name := config.FinalAgentName
	if len(args) > 1 {
		name = strings.TrimSpace(args[1])
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	enabled := state == "on"
	switch kind {
	case config.ChannelTelegram:
		entry, ok := cfg.TelegramAccount(name)
		if !ok {
			return fmt.Errorf("no telegram account %q", name)
		}
		entry.Enabled = enabled
		cfg = cfg.WithTelegramAccount(name, entry)
	case config.ChannelWhatsApp:
		entry, ok := cfg.WhatsAppAccount(name)
		if !ok {
			return fmt.Errorf("no whatsapp account %q", name)
		}
		entry.Enabled = enabled
		cfg = cfg.WithWhatsAppAccount(name, entry)
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s account %q enabled=%v\n", kind, name, enabled)
	return nil
}

func statusChannel(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return listChannels(nil, stdout)
	}
	kind, err := channelType(args[0])
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	names := []string{}
	if kind == config.ChannelTelegram {
		for name := range cfg.Channels.Telegram.Accounts {
			names = append(names, name)
		}
	} else {
		for name := range cfg.Channels.WhatsApp.Accounts {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(args) > 1 {
		names = []string{strings.TrimSpace(args[1])}
	}
	for _, name := range names {
		switch kind {
		case config.ChannelTelegram:
			entry, ok := cfg.TelegramAccount(name)
			if !ok {
				return fmt.Errorf("no telegram account %q", name)
			}
			fmt.Fprintf(stdout, "telegram/%s\n", name)
			fmt.Fprintf(stdout, "  agent:      %s\n", entry.AgentName(name))
			fmt.Fprintf(stdout, "  enabled:    %v\n", entry.Enabled)
			fmt.Fprintf(stdout, "  state:      %s\n", telegramState(cfg, name, entry))
			fmt.Fprintf(stdout, "  dmPolicy:   %s (%d allowlisted)\n", config.NormalizeDMPolicy(entry.DMPolicy), len(entry.AllowFrom))
			fmt.Fprintf(stdout, "  groupPolicy: %s (%d groups listed)\n", config.NormalizeGroupPolicy(entry.GroupPolicy), len(entry.GroupAllowFrom))
		case config.ChannelWhatsApp:
			entry, ok := cfg.WhatsAppAccount(name)
			if !ok {
				return fmt.Errorf("no whatsapp account %q", name)
			}
			fmt.Fprintf(stdout, "whatsapp/%s\n", name)
			fmt.Fprintf(stdout, "  agent:      %s\n", entry.AgentName(name))
			fmt.Fprintf(stdout, "  enabled:    %v\n", entry.Enabled)
			fmt.Fprintf(stdout, "  state:      %s\n", whatsappState(cfg, name))
			fmt.Fprintf(stdout, "  dmPolicy:   %s (%d allowlisted)\n", config.NormalizeDMPolicy(entry.DMPolicy), len(entry.AllowFrom))
			fmt.Fprintf(stdout, "  groupPolicy: %s (%d groups listed)\n", config.NormalizeGroupPolicy(entry.GroupPolicy), len(entry.GroupAllowFrom))
		}
	}
	return nil
}

func removeChannel(args []string, stdout io.Writer) error {
	purge := false
	var positionals []string
	for _, arg := range args {
		switch arg {
		case "--purge":
			purge = true
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("unknown channel rm option %q", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) < 1 {
		return fmt.Errorf("usage: termixgo channel rm <whatsapp|telegram> [agent] [--purge]")
	}
	kind, err := channelType(positionals[0])
	if err != nil {
		return err
	}
	name := config.FinalAgentName
	if len(positionals) > 1 {
		name = strings.TrimSpace(positionals[1])
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	updated, ok := cfg.WithoutChannelAccount(kind, name)
	if !ok {
		return fmt.Errorf("no %s account %q", kind, name)
	}
	if kind == config.ChannelTelegram {
		if store, err := secrets.Load(); err == nil {
			_ = store.Delete(secrets.TelegramAccountTokenKey(name))
		}
	}
	if err := config.Save(updated); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s account %q removed\n", kind, name)
	if purge && kind == config.ChannelWhatsApp {
		agent := cfg.Channels.WhatsApp.Accounts[name].AgentName(name)
		if agentPaths, err := agenthome.Resolve(agent, cfg.Agent(agent).Workspace); err == nil {
			dir := filepath.Join(agentPaths.Home, "channels", "whatsapp", name)
			if err := os.RemoveAll(dir); err == nil {
				fmt.Fprintf(stdout, "  session files deleted: %s\n", dir)
			}
		}
	}
	return nil
}

func editChannelList(args []string, verb string, stdout io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: termixgo channel %s add|rm <whatsapp|telegram> [agent] <id...>", verb)
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	kind, err := channelType(args[1])
	if err != nil {
		return err
	}
	rest := args[2:]
	name := config.FinalAgentName
	if len(rest) > 1 {
		name = strings.TrimSpace(rest[0])
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: termixgo channel %s add|rm <whatsapp|telegram> [agent] <id...>", verb)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	apply := func(list []string) []string {
		if action == "rm" || action == "remove" || action == "del" {
			for _, id := range rest {
				list = channel.RemoveID(list, id)
			}
			return list
		}
		for _, id := range rest {
			list = channel.AddID(list, id)
		}
		return list
	}
	assign := func(entry config.ChannelAccount) config.ChannelAccount {
		if verb == "admin" {
			entry.Admins = apply(entry.Admins)
		} else {
			entry.AllowFrom = apply(entry.AllowFrom)
		}
		return entry
	}
	switch kind {
	case config.ChannelTelegram:
		entry, ok := cfg.TelegramAccount(name)
		if !ok {
			return fmt.Errorf("no telegram account %q", name)
		}
		entry.ChannelAccount = assign(entry.ChannelAccount)
		cfg = cfg.WithTelegramAccount(name, entry)
	case config.ChannelWhatsApp:
		entry, ok := cfg.WhatsAppAccount(name)
		if !ok {
			return fmt.Errorf("no whatsapp account %q", name)
		}
		cfg = cfg.WithWhatsAppAccount(name, assign(entry))
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s %s/%s updated (%s: %s)\n", verb, kind, name, action, strings.Join(rest, ", "))
	return nil
}

func editChannelGroups(args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: termixgo channel groups <whatsapp|telegram> [agent] <mention|all|disabled|allow add|rm <chatid>>")
	}
	kind, err := channelType(args[0])
	if err != nil {
		return err
	}
	rest := args[1:]
	name := config.FinalAgentName
	if len(rest) > 1 && !isGroupVerb(rest[0]) {
		name = strings.TrimSpace(rest[0])
		rest = rest[1:]
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	update := func(entry config.ChannelAccount) (config.ChannelAccount, error) {
		switch strings.ToLower(strings.TrimSpace(rest[0])) {
		case "mention", "all", "disabled":
			entry.GroupPolicy = strings.ToLower(strings.TrimSpace(rest[0]))
			return entry, nil
		case "allow":
			if len(rest) < 3 {
				return entry, fmt.Errorf("usage: termixgo channel groups <type> [agent] allow add|rm <chatid>")
			}
			action := strings.ToLower(strings.TrimSpace(rest[1]))
			list := entry.GroupAllowFrom
			if action == "rm" || action == "remove" || action == "del" {
				for _, id := range rest[2:] {
					list = channel.RemoveID(list, id)
				}
			} else {
				for _, id := range rest[2:] {
					list = channel.AddID(list, id)
				}
			}
			entry.GroupAllowFrom = list
			return entry, nil
		default:
			return entry, fmt.Errorf("unknown groups value %q; use mention, all, disabled or allow", rest[0])
		}
	}
	switch kind {
	case config.ChannelTelegram:
		entry, ok := cfg.TelegramAccount(name)
		if !ok {
			return fmt.Errorf("no telegram account %q", name)
		}
		updated, err := update(entry.ChannelAccount)
		if err != nil {
			return err
		}
		entry.ChannelAccount = updated
		cfg = cfg.WithTelegramAccount(name, entry)
	case config.ChannelWhatsApp:
		entry, ok := cfg.WhatsAppAccount(name)
		if !ok {
			return fmt.Errorf("no whatsapp account %q", name)
		}
		updated, err := update(entry)
		if err != nil {
			return err
		}
		cfg = cfg.WithWhatsAppAccount(name, updated)
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "group policy for %s/%s updated\n", kind, name)
	return nil
}

func isGroupVerb(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "mention", "all", "disabled", "allow":
		return true
	}
	return false
}

func editChannelGuest(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: termixgo channel guest <whatsapp|telegram> [agent] [--max-steps N] [--cost-budget N] [--rate N]")
	}
	kind, err := channelType(args[0])
	if err != nil {
		return err
	}
	rest := args[1:]
	name := config.FinalAgentName
	var maxSteps, rate int
	var cost float64
	stepsSet, costSet, rateSet := false, false, false
	for index := 0; index < len(rest); index++ {
		arg := rest[index]
		if !strings.HasPrefix(arg, "-") {
			if name != config.FinalAgentName {
				return fmt.Errorf("usage: termixgo channel guest <type> [agent] [--max-steps N] [--cost-budget N] [--rate N]")
			}
			name = strings.TrimSpace(arg)
			continue
		}
		if index+1 >= len(rest) {
			return fmt.Errorf("%s needs a value", arg)
		}
		index++
		switch arg {
		case "--max-steps":
			if _, err := fmt.Sscanf(rest[index], "%d", &maxSteps); err != nil {
				return fmt.Errorf("--max-steps needs a number")
			}
			stepsSet = true
		case "--cost-budget":
			if _, err := fmt.Sscanf(rest[index], "%f", &cost); err != nil {
				return fmt.Errorf("--cost-budget needs a number")
			}
			costSet = true
		case "--rate":
			if _, err := fmt.Sscanf(rest[index], "%d", &rate); err != nil {
				return fmt.Errorf("--rate needs a number")
			}
			rateSet = true
		default:
			return fmt.Errorf("unknown channel guest option %q", arg)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	update := func(entry config.ChannelAccount) config.ChannelAccount {
		limits := entry.EffectiveGuestLimits()
		if stepsSet {
			limits.MaxSteps = maxSteps
		}
		if costSet {
			limits.CostBudgetUSD = cost
		}
		if rateSet {
			limits.RatePerMinute = rate
		}
		return entry.SetGuestLimits(limits)
	}
	var limits config.GuestLimits
	switch kind {
	case config.ChannelTelegram:
		entry, ok := cfg.TelegramAccount(name)
		if !ok {
			return fmt.Errorf("no telegram account %q", name)
		}
		entry.ChannelAccount = update(entry.ChannelAccount)
		limits = entry.EffectiveGuestLimits()
		cfg = cfg.WithTelegramAccount(name, entry)
	case config.ChannelWhatsApp:
		entry, ok := cfg.WhatsAppAccount(name)
		if !ok {
			return fmt.Errorf("no whatsapp account %q", name)
		}
		entry = update(entry)
		limits = entry.EffectiveGuestLimits()
		cfg = cfg.WithWhatsAppAccount(name, entry)
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "guest limits for %s/%s: maxSteps=%d costBudgetUsd=%.2f ratePerMinute=%d\n",
		kind, name, limits.MaxSteps, limits.CostBudgetUSD, limits.RatePerMinute)
	return nil
}

func pairingCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 || strings.EqualFold(strings.TrimSpace(args[0]), "list") {
		path, err := channel.DefaultPairingPath()
		if err != nil {
			return err
		}
		store, err := channel.OpenPairingStore(path)
		if err != nil {
			return err
		}
		pending := store.Pending()
		if len(pending) == 0 {
			fmt.Fprintln(stdout, "No pending pairing requests.")
			return nil
		}
		for _, request := range pending {
			fmt.Fprintf(stdout, "%s  %-9s %-12s %s  (%s)\n", request.Code, request.Channel, request.Account, request.SenderID, request.SenderName)
		}
		return nil
	}
	action := strings.ToLower(strings.TrimSpace(args[0]))
	if len(args) < 2 {
		return fmt.Errorf("usage: termixgo channel pairing <list|approve|deny> <code>")
	}
	code := strings.TrimSpace(args[1])
	path, err := channel.DefaultPairingPath()
	if err != nil {
		return err
	}
	store, err := channel.OpenPairingStore(path)
	if err != nil {
		return err
	}
	var request channel.PairingRequest
	var ok bool
	switch action {
	case "approve":
		request, ok = store.Approve(code)
	case "deny", "reject":
		request, ok = store.Deny(code)
	default:
		return fmt.Errorf("usage: termixgo channel pairing <list|approve|deny> <code>")
	}
	if !ok {
		return fmt.Errorf("no pairing request with code %q", code)
	}
	if action != "approve" {
		fmt.Fprintf(stdout, "pairing request from %s denied\n", request.SenderID)
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch request.Channel {
	case config.ChannelTelegram:
		entry, exists := cfg.TelegramAccount(request.Account)
		if !exists {
			return fmt.Errorf("the account no longer exists")
		}
		entry.AllowFrom = channel.AddID(entry.AllowFrom, request.SenderID)
		cfg = cfg.WithTelegramAccount(request.Account, entry)
	case config.ChannelWhatsApp:
		entry, exists := cfg.WhatsAppAccount(request.Account)
		if !exists {
			return fmt.Errorf("the account no longer exists")
		}
		entry.AllowFrom = channel.AddID(entry.AllowFrom, request.SenderID)
		cfg = cfg.WithWhatsAppAccount(request.Account, entry)
	default:
		return fmt.Errorf("unknown channel %q in the request", request.Channel)
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s (%s/%s) is now allowlisted\n", request.SenderID, request.Channel, request.Account)
	return nil
}
