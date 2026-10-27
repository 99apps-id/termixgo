package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
)

// typingRefresh is how often the composing presence is renewed. WhatsApp drops
// the indicator quickly, so a long turn would otherwise look stopped.
const typingRefresh = 4 * time.Second

// Bot serves one WhatsApp account: it normalizes inbound messages, applies the
// account policy, runs the turn through the bridge, and sends the answer.
//
// Replies are sent as conversation only: no tool lines, no working card, no
// file paths. The account is a person's chat, so it reads like one.
type Bot struct {
	client *whatsmeow.Client
	agent  channel.Agent

	mu       sync.RWMutex
	account  string
	policy   config.ChannelAccount
	pairing  *channel.PairingStore
	selfIDs  map[string]bool
	limiter  *channel.RateLimiter
	logf     func(string)
	sendHook func(chatID, text string)
}

// New builds the bot for one client and bridge.
func New(client *whatsmeow.Client, agent channel.Agent) *Bot {
	return &Bot{client: client, agent: agent}
}

// SetAccount installs the account identity, policy and pairing store.
func (b *Bot) SetAccount(name string, policy config.ChannelAccount, pairing *channel.PairingStore) {
	b.mu.Lock()
	b.account = strings.TrimSpace(name)
	b.policy = policy
	b.pairing = pairing
	b.limiter = channel.ForAccount(policy)
	b.mu.Unlock()
}

// SetLogger receives diagnostic lines; nil discards them.
func (b *Bot) SetLogger(logf func(string)) {
	b.mu.Lock()
	b.logf = logf
	b.mu.Unlock()
}

// Run registers the event handlers and keeps the connection until the context
// ends. whatsmeow reconnects on its own; a logout is reported and left for the
// operator to relink.
func (b *Bot) Run(ctx context.Context) error {
	if b.client == nil {
		return fmt.Errorf("no WhatsApp client")
	}
	b.refreshSelf()
	b.client.AddEventHandler(func(evt any) {
		switch event := evt.(type) {
		case *events.Message:
			go b.handleMessage(ctx, event)
		case *events.Connected:
			b.refreshSelf()
		case *events.LoggedOut:
			b.logf("logged out; run 'termixgo channel login whatsapp " + b.accountName() + "'")
		}
	})
	if err := b.client.Connect(); err != nil {
		return fmt.Errorf("connect WhatsApp: %w", err)
	}
	<-ctx.Done()
	b.client.Disconnect()
	return ctx.Err()
}

// SelfID is the linked number as a JID string, or "" before login.
func (b *Bot) SelfID() string {
	if b.client == nil || b.client.Store == nil || b.client.Store.ID == nil {
		return ""
	}
	return b.client.Store.ID.String()
}

func (b *Bot) accountName() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.account
}

func (b *Bot) policySnapshot() config.ChannelAccount {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.policy
}

func (b *Bot) logfLine(line string) {
	b.mu.RLock()
	logf := b.logf
	b.mu.RUnlock()
	if logf != nil {
		logf(line)
	}
}

// refreshSelf records the account's own JIDs, which is what a group mention is
// matched against. Both the phone-number form and the LID form are kept: a
// mention may arrive in either.
func (b *Bot) refreshSelf() {
	if b.client == nil || b.client.Store == nil {
		return
	}
	set := map[string]bool{}
	if b.client.Store.ID != nil {
		set[channel.CanonicalID(b.client.Store.ID.String())] = true
	}
	if !b.client.Store.LID.IsEmpty() {
		set[channel.CanonicalID(b.client.Store.LID.String())] = true
	}
	b.mu.Lock()
	b.selfIDs = set
	b.mu.Unlock()
}

func (b *Bot) isSelf(senderID string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.selfIDs[channel.CanonicalID(senderID)]
}

func (b *Bot) handleMessage(ctx context.Context, evt *events.Message) {
	if evt == nil {
		return
	}
	if evt.Info.IsFromMe {
		// Ignore outbound messages to other chats, or messages sent by this companion bot instance.
		selfUser := ""
		selfDevice := uint16(0)
		if b.client != nil && b.client.Store != nil && b.client.Store.ID != nil {
			selfUser = b.client.Store.ID.User
			selfDevice = b.client.Store.ID.Device
		}
		isSelfChat := selfUser != "" && evt.Info.Chat.User == selfUser
		isOwnDevice := selfDevice != 0 && evt.Info.Sender.Device == selfDevice
		if !isSelfChat || isOwnDevice {
			return
		}
	}
	request := b.requestFrom(evt)
	if strings.TrimSpace(request.Text) == "" {
		// Media is a later phase; a caption-free image has nothing to run.
		return
	}
	policy := b.policySnapshot()
	decision := channel.Decide(policy, request)
	if b.isSelf(request.SenderID) || (request.SenderAlt != "" && b.isSelf(request.SenderAlt)) {
		// The account's own number is the operator.
		decision = channel.Decision{Allow: true, Admin: true, Reason: "operator"}
	}
	if !decision.Allow {
		if b.mayPair(policy, request) {
			b.reply(ctx, request.ChatID, b.pairingPrompt(request))
			return
		}
		b.logfLine("ignored message from " + request.SenderID + " (" + decision.Reason + ")")
		return
	}
	lang := b.userLanguage(request)
	request.Admin = decision.Admin
	if !decision.Admin {
		b.mu.RLock()
		limiter := b.limiter
		b.mu.RUnlock()
		if limiter != nil && !limiter.Allow(request.SenderID, time.Now()) {
			if lang == "id" {
				b.reply(ctx, request.ChatID, "Terlalu banyak pesan. Coba lagi sebentar ya.")
			} else {
				b.reply(ctx, request.ChatID, "Too many messages. Please try again in a moment.")
			}
			return
		}
	}
	if strings.HasPrefix(strings.TrimSpace(request.Text), "/") {
		if !decision.Admin {
			if lang == "id" {
				b.reply(ctx, request.ChatID, "Maaf, perintah itu hanya untuk operator.")
			} else {
				b.reply(ctx, request.ChatID, "Sorry, that command is restricted to the operator.")
			}
			return
		}
		b.handleCommand(ctx, request)
		return
	}

	done := make(chan struct{})
	go b.keepTyping(ctx, request.ChatID, done)
	answer, err := b.agent.RunPrompt(ctx, request, request.Text, nil)
	close(done)
	if err != nil {
		b.reply(ctx, request.ChatID, naturalError(err, lang))
		return
	}
	if channel.Silent(answer) {
		return
	}
	for _, chunk := range channel.Chunk(answer, policy.EffectiveTextChunkLimit()) {
		b.reply(ctx, request.ChatID, chunk)
	}
}

// requestFrom normalizes one WhatsApp message into the shared request.
func (b *Bot) requestFrom(evt *events.Message) channel.Request {
	info := evt.Info
	senderAlt := ""
	if !info.SenderAlt.IsEmpty() {
		senderAlt = info.SenderAlt.String()
	} else if b.client != nil && b.client.Store != nil {
		if alt, err := b.client.Store.GetAltJID(context.Background(), info.Sender); err == nil && !alt.IsEmpty() {
			senderAlt = alt.String()
		}
	}
	request := channel.Request{
		Channel:    config.ChannelWhatsApp,
		Account:    b.accountName(),
		ChatID:     info.Chat.String(),
		IsGroup:    info.IsGroup || info.Chat.Server == types.GroupServer,
		SenderID:   info.Sender.String(),
		SenderAlt:  senderAlt,
		SenderName: strings.TrimSpace(info.PushName),
		Text:       messageText(evt.Message),
	}
	request.MentionsBot = b.mentionsSelf(evt.Message)
	return request
}

// mentionsSelf reports whether the message names this account, in either the
// phone-number or the LID form.
func (b *Bot) mentionsSelf(msg *waE2E.Message) bool {
	contextInfo := mentionContext(msg)
	if contextInfo == nil {
		return false
	}
	b.mu.RLock()
	self := b.selfIDs
	b.mu.RUnlock()
	for _, mentioned := range contextInfo.GetMentionedJID() {
		if self[channel.CanonicalID(mentioned)] {
			return true
		}
	}
	return false
}

func mentionContext(msg *waE2E.Message) *waE2E.ContextInfo {
	if msg == nil {
		return nil
	}
	if extended := msg.GetExtendedTextMessage(); extended != nil {
		return extended.GetContextInfo()
	}
	if image := msg.GetImageMessage(); image != nil {
		return image.GetContextInfo()
	}
	if document := msg.GetDocumentMessage(); document != nil {
		return document.GetContextInfo()
	}
	return nil
}

// messageText is the text a message carries, or "" when it carries none.
func messageText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if text := strings.TrimSpace(msg.GetConversation()); text != "" {
		return text
	}
	if extended := msg.GetExtendedTextMessage(); extended != nil {
		return strings.TrimSpace(extended.GetText())
	}
	return ""
}

// mayPair reports whether an unknown DM sender should receive a pairing code.
func (b *Bot) mayPair(policy config.ChannelAccount, request channel.Request) bool {
	if request.IsGroup || config.NormalizeDMPolicy(policy.DMPolicy) != config.DMPolicyPairing {
		return false
	}
	b.mu.RLock()
	store := b.pairing
	b.mu.RUnlock()
	return store != nil
}

func (b *Bot) userLanguage(request channel.Request) string {
	sender := request.SenderID
	if idx := strings.Index(sender, "@"); idx > 0 {
		sender = sender[:idx]
	}
	var digits strings.Builder
	for _, r := range sender {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	phone := digits.String()
	if strings.HasPrefix(phone, "62") {
		return "id"
	}
	if request.SenderAlt != "" {
		alt := request.SenderAlt
		if idx := strings.Index(alt, "@"); idx > 0 {
			alt = alt[:idx]
		}
		var altDigits strings.Builder
		for _, r := range alt {
			if r >= '0' && r <= '9' {
				altDigits.WriteRune(r)
			}
		}
		if strings.HasPrefix(altDigits.String(), "62") {
			return "id"
		}
	}
	return "en"
}

func (b *Bot) pairingPrompt(request channel.Request) string {
	b.mu.RLock()
	store := b.pairing
	b.mu.RUnlock()
	pending, created := store.Request(config.ChannelWhatsApp, request.Account, request.SenderID, request.SenderName)
	if created {
		b.logfLine("pairing request from " + request.SenderID + " (" + pending.Code + ")")
	}
	if b.userLanguage(request) == "id" {
		return "Halo! Percakapan ini belum diizinkan. Sandi pairing kamu: " + pending.Code +
			"\nMinta operator menyetujui dengan: termixgo channel pairing approve " + pending.Code
	}
	return "Hello! This conversation is not yet authorized. Your pairing code: " + pending.Code +
		"\nAsk the operator to approve with: termixgo channel pairing approve " + pending.Code
}

// SendTo posts one message to one chat, chunked to the account's limit. It is
// how a scheduled job reaches a chat outside the normal turn.
func (b *Bot) SendTo(ctx context.Context, chatID, text string) error {
	jid, err := types.ParseJID(chatID)
	if err != nil {
		return fmt.Errorf("bad chat id %q: %w", chatID, err)
	}
	for _, chunk := range channel.Chunk(text, b.policySnapshot().EffectiveTextChunkLimit()) {
		if _, err := b.client.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(chunk)}); err != nil {
			return err
		}
	}
	return nil
}

// reply sends one message. A failure is logged, never surfaced to the chat.
func (b *Bot) reply(ctx context.Context, chatID, text string) {
	b.mu.RLock()
	hook := b.sendHook
	b.mu.RUnlock()
	if hook != nil {
		hook(chatID, text)
		return
	}
	if b.client == nil {
		b.logfLine("no client to reply: " + text)
		return
	}
	jid, err := types.ParseJID(chatID)
	if err != nil {
		b.logfLine("bad chat id " + chatID + ": " + err.Error())
		return
	}
	_, err = b.client.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(text)})
	if err != nil {
		b.logfLine("send failed: " + err.Error())
	}
}

// keepTyping holds the composing indicator while the turn runs, which is the
// whole progress story on WhatsApp: a person sees "typing", then one answer.
func (b *Bot) keepTyping(ctx context.Context, chatID string, done <-chan struct{}) {
	jid, err := types.ParseJID(chatID)
	if err != nil {
		return
	}
	compose := func() {
		_ = b.client.SendChatPresence(ctx, jid, types.ChatPresenceComposing, types.ChatPresenceMediaText)
	}
	compose()
	ticker := time.NewTicker(typingRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			_ = b.client.SendChatPresence(context.Background(), jid, types.ChatPresencePaused, types.ChatPresenceMediaText)
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			compose()
		}
	}
}

// naturalError turns a failure into one human sentence. Tool names, paths and
// internal errors never reach a chat.
func naturalError(err error, lang ...string) string {
	l := "id"
	if len(lang) > 0 {
		l = lang[0]
	}
	if l == "id" {
		switch {
		case errors.Is(err, channel.ErrBusy):
			return "Sebentar ya, aku masih menyelesaikan permintaan sebelumnya. Coba lagi sesaat lagi."
		case errors.Is(err, context.Canceled):
			return "Permintaan dibatalkan."
		default:
			return "Maaf, ada gangguan sebentar. Coba lagi ya."
		}
	}
	switch {
	case errors.Is(err, channel.ErrBusy):
		return "Please wait a moment, I am still finishing the previous request. Please try again shortly."
	case errors.Is(err, context.Canceled):
		return "Request was canceled."
	default:
		return "Sorry, an error occurred. Please try again."
	}
}

// DiffProvider is an optional interface that an agent may implement to provide workspace diffs.
type DiffProvider interface {
	GitDiff(ctx context.Context) (string, error)
}

func (b *Bot) handleCommand(ctx context.Context, request channel.Request) {
	command, argument := splitCommand(request.Text)
	lang := b.userLanguage(request)
	switch command {
	case "help", "menu", "start":
		b.sendReply(ctx, request.ChatID, b.helpText(lang))
	case "models":
		if lister, ok := b.agent.(interface{ ModelsFor(string) string }); ok {
			b.sendReply(ctx, request.ChatID, lister.ModelsFor(lang))
		} else {
			b.sendReply(ctx, request.ChatID, b.agent.Models())
		}
	case "model":
		if strings.TrimSpace(argument) == "" {
			current := b.agent.Model()
			if lang == "id" {
				b.sendReply(ctx, request.ChatID, "Model aktif saat ini: "+current+"\n\nKetik /model <nama> untuk beralih model.\nContoh: /model stepfun, /model muse, /model antigravity.\nKetik /models untuk melihat semua model.")
			} else {
				b.sendReply(ctx, request.ChatID, "Current active model: "+current+"\n\nType /model <name> to switch models.\nExamples: /model stepfun, /model muse, /model antigravity.\nType /models to view all available models.")
			}
			return
		}
		updated, err := b.agent.SetModel(argument)
		if err != nil {
			if lang == "id" {
				b.sendReply(ctx, request.ChatID, "Gagal mengganti model: "+err.Error())
			} else {
				b.sendReply(ctx, request.ChatID, "Failed to switch model: "+err.Error())
			}
			return
		}
		if lang == "id" {
			b.sendReply(ctx, request.ChatID, "Model berhasil diubah ke: "+updated)
		} else {
			b.sendReply(ctx, request.ChatID, "Model successfully switched to: "+updated)
		}
	case "status":
		b.sendReply(ctx, request.ChatID, b.agent.Status())
	case "new", "reset", "clear":
		b.agent.NewSession(request)
		if lang == "id" {
			b.sendReply(ctx, request.ChatID, "Sesi percakapan baru telah dimulai.")
		} else {
			b.sendReply(ctx, request.ChatID, "A new conversation session has been started.")
		}
	case "stop":
		b.agent.Stop()
		if lang == "id" {
			b.sendReply(ctx, request.ChatID, "Perintah berhenti dikirim ke proses yang sedang berjalan.")
		} else {
			b.sendReply(ctx, request.ChatID, "Stop signal sent to the running process.")
		}
	case "diff":
		diffProv, ok := b.agent.(DiffProvider)
		if !ok {
			if lang == "id" {
				b.sendReply(ctx, request.ChatID, "Fitur diff tidak didukung oleh agent ini.")
			} else {
				b.sendReply(ctx, request.ChatID, "Diff feature is not supported by this agent.")
			}
			return
		}
		diff, err := diffProv.GitDiff(ctx)
		if err != nil {
			if lang == "id" {
				b.sendReply(ctx, request.ChatID, "Gagal membaca git diff: "+err.Error())
			} else {
				b.sendReply(ctx, request.ChatID, "Failed to read git diff: "+err.Error())
			}
			return
		}
		trimmed := strings.TrimSpace(diff)
		if trimmed == "" {
			if lang == "id" {
				b.sendReply(ctx, request.ChatID, "Tidak ada perubahan git di workspace.")
			} else {
				b.sendReply(ctx, request.ChatID, "No git changes in workspace.")
			}
			return
		}
		if len(trimmed) > 3000 {
			if lang == "id" {
				trimmed = trimmed[:3000] + "\n... (dipotong karena terlalu panjang)"
			} else {
				trimmed = trimmed[:3000] + "\n... (truncated because too long)"
			}
		}
		b.sendReply(ctx, request.ChatID, "```diff\n"+trimmed+"\n```")
	default:
		if lang == "id" {
			b.sendReply(ctx, request.ChatID, "Perintah /"+command+" tidak dikenal. Ketik /menu atau /help untuk melihat daftar perintah.")
		} else {
			b.sendReply(ctx, request.ChatID, "Unknown command /"+command+". Type /menu or /help to view available commands.")
		}
	}
}

func (b *Bot) sendReply(ctx context.Context, chatID, text string) {
	for _, chunk := range channel.Chunk(text, b.policySnapshot().EffectiveTextChunkLimit()) {
		b.reply(ctx, chatID, chunk)
	}
}

func (b *Bot) helpText(lang ...string) string {
	l := "id"
	if len(lang) > 0 {
		l = lang[0]
	}
	if l == "id" {
		return strings.Join([]string{
			"Daftar Perintah Slash Termixgo:",
			"",
			"/models - Lihat daftar model yang terdaftar dan siap digunakan",
			"/model <nama> - Ganti model aktif (misal: /model stepfun, /model muse, /model antigravity)",
			"/status - Status runtime dan resource agent",
			"/new atau /reset - Mulai percakapan sesi baru",
			"/stop - Hentikan proses yang sedang berjalan",
			"/diff - Lihat perubahan git di workspace",
			"/help atau /menu - Menampilkan menu bantuan ini",
		}, "\n")
	}
	return strings.Join([]string{
		"Termixgo Slash Commands:",
		"",
		"/models - List registered and ready-to-use models",
		"/model <name> - Switch active model (e.g. /model stepfun, /model muse, /model antigravity)",
		"/status - Agent runtime and resource status",
		"/new or /reset - Start a fresh conversation session",
		"/stop - Stop currently running process",
		"/diff - View git changes in workspace",
		"/help or /menu - Show this help menu",
	}, "\n")
}

func splitCommand(text string) (string, string) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(text), "/")
	command := trimmed
	rest := ""
	if index := strings.IndexAny(trimmed, " \n\t"); index >= 0 {
		command = trimmed[:index]
		rest = strings.TrimSpace(trimmed[index+1:])
	}
	if index := strings.Index(command, "@"); index > 0 {
		command = command[:index]
	}
	return strings.ToLower(command), rest
}
