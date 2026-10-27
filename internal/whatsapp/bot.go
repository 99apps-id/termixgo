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

	mu      sync.RWMutex
	account string
	policy  config.ChannelAccount
	pairing *channel.PairingStore
	selfIDs map[string]bool
	limiter *channel.RateLimiter
	logf    func(string)
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
	request.Admin = decision.Admin
	if !decision.Admin {
		b.mu.RLock()
		limiter := b.limiter
		b.mu.RUnlock()
		if limiter != nil && !limiter.Allow(request.SenderID, time.Now()) {
			b.reply(ctx, request.ChatID, "Terlalu banyak pesan. Coba lagi sebentar ya.")
			return
		}
	}
	if !decision.Admin && strings.HasPrefix(strings.TrimSpace(request.Text), "/") {
		b.reply(ctx, request.ChatID, "Maaf, perintah itu hanya untuk operator.")
		return
	}

	done := make(chan struct{})
	go b.keepTyping(ctx, request.ChatID, done)
	answer, err := b.agent.RunPrompt(ctx, request, request.Text, nil)
	close(done)
	if err != nil {
		b.reply(ctx, request.ChatID, naturalError(err))
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

func (b *Bot) pairingPrompt(request channel.Request) string {
	b.mu.RLock()
	store := b.pairing
	b.mu.RUnlock()
	pending, created := store.Request(config.ChannelWhatsApp, request.Account, request.SenderID, request.SenderName)
	if created {
		b.logfLine("pairing request from " + request.SenderID + " (" + pending.Code + ")")
	}
	return "Halo! Percakapan ini belum diizinkan. Sandi pairing kamu: " + pending.Code +
		"\nMinta operator menyetujui dengan: termixgo channel pairing approve " + pending.Code
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
func naturalError(err error) string {
	switch {
	case errors.Is(err, channel.ErrBusy):
		return "Sebentar ya, aku masih menyelesaikan permintaan sebelumnya. Coba lagi sesaat lagi."
	case errors.Is(err, context.Canceled):
		return "Permintaan dibatalkan."
	default:
		return "Maaf, ada gangguan sebentar. Coba lagi ya."
	}
}
