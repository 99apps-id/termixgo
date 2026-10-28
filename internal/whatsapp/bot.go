package whatsapp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

// ImageAgent is an agent that can receive an image or document prompt for a vision model.
type ImageAgent interface {
	RunPromptWithImage(ctx context.Context, request channel.Request, prompt, mediaType, data string, progress func(string)) (string, error)
}

// AudioTranscriber is an agent that can transcribe voice/audio bytes to text.
type AudioTranscriber interface {
	TranscribeAudio(ctx context.Context, data []byte, filename string) (string, error)
}

// VoicePromptRunner is an agent that can run a voice-originated prompt with a fallback model.
type VoicePromptRunner interface {
	RunVoicePrompt(ctx context.Context, request channel.Request, prompt string, progress func(string)) (string, error)
}

// SessionInspector is an optional interface that exposes per-chat session cost, plan, and compaction.
type SessionInspector interface {
	CostInfo(request channel.Request, lang string) string
	PlanInfo(request channel.Request, lang string) string
	CompactSession(ctx context.Context, request channel.Request, lang string) string
}

// WorkspaceProvider gives access to the workspace directory path.
type WorkspaceProvider interface {
	Workspace() string
}

// Bot serves one WhatsApp account: it normalizes inbound messages, applies the
// account policy, runs the turn through the bridge, and sends the answer.
//
// Replies are sent as conversation only: no tool lines, no working card, no
// file paths. The account is a person's chat, so it reads like one.
type Bot struct {
	client *whatsmeow.Client
	agent  channel.Agent

	mu           sync.RWMutex
	account      string
	policy       config.ChannelAccount
	pairing      *channel.PairingStore
	selfIDs      map[string]bool
	limiter      *channel.RateLimiter
	logf         func(string)
	sendHook     func(chatID, text string)
	sendFileHook func(chatID, filename string, data []byte, caption string)
	downloadHook func(ctx context.Context, msg whatsmeow.DownloadableMessage) ([]byte, error)
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

	rawMsg := unwrapMessage(evt.Message)
	if rawMsg == nil {
		return
	}
	imageMsg := rawMsg.GetImageMessage()
	docMsg := rawMsg.GetDocumentMessage()
	audioMsg := rawMsg.GetAudioMessage()
	quoted := quotedText(rawMsg)

	request := b.requestFrom(evt)
	if imageMsg != nil {
		request.Text = strings.TrimSpace(imageMsg.GetCaption())
	} else if docMsg != nil {
		request.Text = strings.TrimSpace(docMsg.GetCaption())
	}

	if imageMsg == nil && docMsg == nil && audioMsg == nil && strings.TrimSpace(request.Text) == "" {
		return
	}

	policy := b.policySnapshot()
	decision := channel.Decide(policy, request)
	if b.isSelf(request.SenderID) || (request.SenderAlt != "" && b.isSelf(request.SenderAlt)) {
		// The account's own number is the operator.
		if !request.IsGroup {
			decision = channel.Decision{Allow: true, Admin: true, Reason: "operator"}
		} else {
			decision.Admin = true
		}
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

	if imageMsg != nil || docMsg != nil || audioMsg != nil {
		if !decision.Admin {
			if lang == "id" {
				b.reply(ctx, request.ChatID, "Maaf, pengiriman media dan dokumen hanya untuk operator.")
			} else {
				b.reply(ctx, request.ChatID, "Sorry, sending media and documents is restricted to the operator.")
			}
			return
		}
		if imageMsg != nil {
			b.runImage(ctx, request, imageMsg, quoted, lang, policy)
			return
		}
		if docMsg != nil {
			b.runDocument(ctx, request, docMsg, quoted, lang, policy)
			return
		}
		if audioMsg != nil {
			b.runAudio(ctx, request, audioMsg, quoted, lang, policy)
			return
		}
	}

	if cmdText, isCmd := channel.ExtractCommandText(request.Text); isCmd {
		if !decision.Admin {
			if lang == "id" {
				b.reply(ctx, request.ChatID, "Maaf, perintah itu hanya untuk operator.")
			} else {
				b.reply(ctx, request.ChatID, "Sorry, that command is restricted to the operator.")
			}
			return
		}
		request.Text = cmdText
		b.handleCommand(ctx, request)
		return
	}

	prompt := channel.FormatQuotedPrompt(quoted, request.Text)
	done := make(chan struct{})
	go b.keepTyping(ctx, request.ChatID, done)
	answer, err := b.agent.RunPrompt(ctx, request, prompt, nil)
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

func (b *Bot) downloadMedia(ctx context.Context, msg whatsmeow.DownloadableMessage) ([]byte, error) {
	b.mu.RLock()
	hook := b.downloadHook
	b.mu.RUnlock()
	if hook != nil {
		return hook(ctx, msg)
	}
	if b.client == nil {
		return nil, errors.New("no whatsapp client available")
	}
	return b.client.Download(ctx, msg)
}

// runImage downloads an image and runs vision inference through the agent.
func (b *Bot) runImage(ctx context.Context, request channel.Request, imageMsg *waE2E.ImageMessage, quoted, lang string, policy config.ChannelAccount) {
	prompt := strings.TrimSpace(imageMsg.GetCaption())
	if prompt == "" {
		if lang == "id" {
			prompt = "Jelaskan gambar ini dan berikan informasi atau tindakan penting terkait gambar tersebut."
		} else {
			prompt = "Describe this image and act on anything notable in it."
		}
	}
	prompt = channel.FormatQuotedPrompt(quoted, prompt)
	data, err := b.downloadMedia(ctx, imageMsg)
	if err != nil {
		if lang == "id" {
			b.reply(ctx, request.ChatID, "Gagal mengunduh gambar: "+err.Error())
		} else {
			b.reply(ctx, request.ChatID, "Could not download image: "+err.Error())
		}
		return
	}

	mediaType := imageMsg.GetMimetype()
	if mediaType == "" {
		mediaType = "image/jpeg"
	}

	fileName := fmt.Sprintf("wa_%d.jpg", time.Now().UnixNano())
	if strings.Contains(mediaType, "png") {
		fileName = fmt.Sprintf("wa_%d.png", time.Now().UnixNano())
	}
	if wp, ok := b.agent.(WorkspaceProvider); ok && wp.Workspace() != "" {
		if savedName, rel, saved := channel.SaveUpload(wp.Workspace(), fileName, data); saved {
			prompt = fmt.Sprintf("[Image uploaded: %s saved to %s]\n\n%s", savedName, rel, prompt)
		}
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	done := make(chan struct{})
	go b.keepTyping(ctx, request.ChatID, done)
	var answer string
	var runErr error
	if imgAgent, ok := b.agent.(ImageAgent); ok {
		answer, runErr = imgAgent.RunPromptWithImage(ctx, request, prompt, mediaType, encoded, nil)
	} else {
		answer, runErr = b.agent.RunPrompt(ctx, request, prompt, nil)
	}
	close(done)

	if runErr != nil {
		b.reply(ctx, request.ChatID, naturalError(runErr, lang))
		return
	}
	if channel.Silent(answer) {
		return
	}
	for _, chunk := range channel.Chunk(answer, policy.EffectiveTextChunkLimit()) {
		b.reply(ctx, request.ChatID, chunk)
	}
}

// runDocument downloads a document file and inspects it via vision or workspace tools.
func (b *Bot) runDocument(ctx context.Context, request channel.Request, docMsg *waE2E.DocumentMessage, quoted, lang string, policy config.ChannelAccount) {
	prompt := strings.TrimSpace(docMsg.GetCaption())
	mimeType := strings.ToLower(docMsg.GetMimetype())
	fileName := filepath.Base(docMsg.GetFileName())
	if fileName == "." || fileName == "/" || fileName == "" {
		fileName = "uploaded_document"
	}

	data, err := b.downloadMedia(ctx, docMsg)
	if err != nil {
		if lang == "id" {
			b.reply(ctx, request.ChatID, "Gagal mengunduh dokumen: "+err.Error())
		} else {
			b.reply(ctx, request.ChatID, "Could not download document: "+err.Error())
		}
		return
	}

	if strings.HasPrefix(mimeType, "image/") || isImageExtension(fileName) {
		if prompt == "" {
			if lang == "id" {
				prompt = "Jelaskan gambar ini dan berikan informasi atau tindakan penting terkait gambar tersebut."
			} else {
				prompt = "Describe this image and act on anything notable in it."
			}
		}
		prompt = channel.FormatQuotedPrompt(quoted, prompt)
		if wp, ok := b.agent.(WorkspaceProvider); ok && wp.Workspace() != "" {
			if savedName, rel, saved := channel.SaveUpload(wp.Workspace(), fileName, data); saved {
				prompt = fmt.Sprintf("[Image uploaded: %s saved to %s]\n\n%s", savedName, rel, prompt)
			}
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		mediaType := mimeType
		if mediaType == "" {
			mediaType = "image/jpeg"
		}
		done := make(chan struct{})
		go b.keepTyping(ctx, request.ChatID, done)
		var answer string
		var runErr error
		if imgAgent, ok := b.agent.(ImageAgent); ok {
			answer, runErr = imgAgent.RunPromptWithImage(ctx, request, prompt, mediaType, encoded, nil)
		} else {
			answer, runErr = b.agent.RunPrompt(ctx, request, prompt, nil)
		}
		close(done)
		if runErr != nil {
			b.reply(ctx, request.ChatID, naturalError(runErr, lang))
			return
		}
		if channel.Silent(answer) {
			return
		}
		for _, chunk := range channel.Chunk(answer, policy.EffectiveTextChunkLimit()) {
			b.reply(ctx, request.ChatID, chunk)
		}
		return
	}

	uploadRelPath := filepath.ToSlash(filepath.Join(".termixgo", "uploads", fileName))
	savedToWorkspace := false
	if wp, ok := b.agent.(WorkspaceProvider); ok && wp.Workspace() != "" {
		if savedName, rel, saved := channel.SaveUpload(wp.Workspace(), fileName, data); saved {
			fileName = savedName
			uploadRelPath = rel
			savedToWorkspace = true
		}
	}

	var builder strings.Builder
	if savedToWorkspace {
		fmt.Fprintf(&builder, "[Document uploaded: %s saved to %s]\n\n", fileName, uploadRelPath)
	} else {
		fmt.Fprintf(&builder, "[Document uploaded: %s (%d bytes)]\n\n", fileName, len(data))
	}

	isPDF := mimeType == "application/pdf" || strings.HasSuffix(strings.ToLower(fileName), ".pdf")
	if isPDF && isPDFVisionModel(b.agent.Model()) {
		if prompt != "" {
			builder.WriteString(channel.FormatQuotedPrompt(quoted, prompt))
		} else {
			defaultPrompt := "Please inspect and analyze this uploaded PDF document."
			if lang == "id" {
				defaultPrompt = "Tolong periksa dan analisa dokumen PDF yang diunggah ini."
			}
			builder.WriteString(channel.FormatQuotedPrompt(quoted, defaultPrompt))
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		done := make(chan struct{})
		go b.keepTyping(ctx, request.ChatID, done)
		var answer string
		var runErr error
		if imgAgent, ok := b.agent.(ImageAgent); ok {
			answer, runErr = imgAgent.RunPromptWithImage(ctx, request, builder.String(), "application/pdf", encoded, nil)
		} else {
			answer, runErr = b.agent.RunPrompt(ctx, request, builder.String(), nil)
		}
		close(done)
		if runErr != nil {
			b.reply(ctx, request.ChatID, naturalError(runErr, lang))
			return
		}
		if channel.Silent(answer) {
			return
		}
		for _, chunk := range channel.Chunk(answer, policy.EffectiveTextChunkLimit()) {
			b.reply(ctx, request.ChatID, chunk)
		}
		return
	}

	if utf8.Valid(data) && len(data) <= 64*1024 {
		builder.WriteString("File content:\n```\n")
		builder.WriteString(string(data))
		builder.WriteString("\n```\n\n")
	}

	if prompt != "" {
		builder.WriteString(channel.FormatQuotedPrompt(quoted, prompt))
	} else {
		defaultPrompt := "Please inspect the uploaded document and proceed."
		if lang == "id" {
			defaultPrompt = "Silakan periksa dokumen yang diunggah dan lanjutkan."
		}
		builder.WriteString(channel.FormatQuotedPrompt(quoted, defaultPrompt))
	}

	done := make(chan struct{})
	go b.keepTyping(ctx, request.ChatID, done)
	answer, err := b.agent.RunPrompt(ctx, request, builder.String(), nil)
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

// runAudio downloads a WhatsApp voice note or audio clip, transcribes it, and runs the turn.
func (b *Bot) runAudio(ctx context.Context, request channel.Request, audioMsg *waE2E.AudioMessage, quoted, lang string, policy config.ChannelAccount) {
	transcriber, ok := b.agent.(AudioTranscriber)
	if !ok {
		if lang == "id" {
			b.reply(ctx, request.ChatID, "Transkripsi pesan suara belum didukung oleh agent ini.")
		} else {
			b.reply(ctx, request.ChatID, "Voice transcription is not supported by this agent.")
		}
		return
	}
	data, err := b.downloadMedia(ctx, audioMsg)
	if err != nil {
		if lang == "id" {
			b.reply(ctx, request.ChatID, "Gagal mengunduh pesan suara: "+err.Error())
		} else {
			b.reply(ctx, request.ChatID, "Could not download voice note: "+err.Error())
		}
		return
	}
	ext := ".ogg"
	mimeType := strings.ToLower(audioMsg.GetMimetype())
	switch {
	case strings.Contains(mimeType, "mp4"), strings.Contains(mimeType, "m4a"), strings.Contains(mimeType, "aac"):
		ext = ".m4a"
	case strings.Contains(mimeType, "mpeg"), strings.Contains(mimeType, "mp3"):
		ext = ".mp3"
	case strings.Contains(mimeType, "wav"):
		ext = ".wav"
	}
	fileName := fmt.Sprintf("voice_%d%s", time.Now().UnixNano(), ext)

	done := make(chan struct{})
	go b.keepTyping(ctx, request.ChatID, done)
	transcript, err := transcriber.TranscribeAudio(ctx, data, fileName)
	if err != nil {
		close(done)
		if lang == "id" {
			b.reply(ctx, request.ChatID, "Gagal menerjemahkan pesan suara: "+err.Error())
		} else {
			b.reply(ctx, request.ChatID, "Voice transcription failed: "+err.Error())
		}
		return
	}
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		close(done)
		if lang == "id" {
			b.reply(ctx, request.ChatID, "Tidak ada ucapan yang terdeteksi pada pesan suara.")
		} else {
			b.reply(ctx, request.ChatID, "No speech detected in the voice note.")
		}
		return
	}
	prompt := channel.FormatQuotedPrompt(quoted, transcript)
	var answer string
	var runErr error
	if vr, ok := b.agent.(VoicePromptRunner); ok {
		answer, runErr = vr.RunVoicePrompt(ctx, request, prompt, nil)
	} else {
		answer, runErr = b.agent.RunPrompt(ctx, request, prompt, nil)
	}
	close(done)
	if runErr != nil {
		b.reply(ctx, request.ChatID, naturalError(runErr, lang))
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
	rawMsg := unwrapMessage(evt.Message)
	request := channel.Request{
		Channel:    config.ChannelWhatsApp,
		Account:    b.accountName(),
		ChatID:     info.Chat.String(),
		IsGroup:    info.IsGroup || info.Chat.Server == types.GroupServer,
		SenderID:   info.Sender.String(),
		SenderAlt:  senderAlt,
		SenderName: strings.TrimSpace(info.PushName),
		Text:       messageText(rawMsg),
	}
	request.MentionsBot = b.mentionsSelf(rawMsg)
	return request
}

// mentionsSelf reports whether the message names this account, in either the
// phone-number or the LID form.
func (b *Bot) mentionsSelf(msg *waE2E.Message) bool {
	contextInfo := mentionContext(unwrapMessage(msg))
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
	if contextInfo.GetQuotedMessage() != nil && self[channel.CanonicalID(contextInfo.GetParticipant())] {
		return true
	}
	return false
}

func mentionContext(msg *waE2E.Message) *waE2E.ContextInfo {
	msg = unwrapMessage(msg)
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
	if audio := msg.GetAudioMessage(); audio != nil {
		return audio.GetContextInfo()
	}
	return nil
}

// quotedText extracts the text or caption of a replied-to WhatsApp message.
func quotedText(msg *waE2E.Message) string {
	ctxInfo := mentionContext(msg)
	if ctxInfo == nil || ctxInfo.GetQuotedMessage() == nil {
		return ""
	}
	quoted := unwrapMessage(ctxInfo.GetQuotedMessage())
	if text := messageText(quoted); text != "" {
		return text
	}
	if image := quoted.GetImageMessage(); image != nil {
		return strings.TrimSpace(image.GetCaption())
	}
	if document := quoted.GetDocumentMessage(); document != nil {
		return strings.TrimSpace(document.GetCaption())
	}
	return ""
}

// messageText is the text a message carries, or "" when it carries none.
func messageText(msg *waE2E.Message) string {
	msg = unwrapMessage(msg)
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

// Connected reports whether the underlying WhatsApp client is connected
// or a test hook is installed.
func (b *Bot) Connected() bool {
	if b == nil {
		return false
	}
	b.mu.RLock()
	hasHook := b.sendHook != nil || b.sendFileHook != nil
	b.mu.RUnlock()
	if hasHook {
		return true
	}
	return b.client != nil && b.client.IsConnected()
}

// NormalizeJID converts a phone number or JID string into a standard WhatsApp JID.
func NormalizeJID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.Contains(trimmed, "@") {
		return trimmed
	}
	var digits strings.Builder
	for _, r := range trimmed {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	num := digits.String()
	if strings.HasPrefix(num, "08") {
		num = "62" + num[1:]
	}
	if num == "" {
		return trimmed
	}
	return num + "@s.whatsapp.net"
}

// SendTo posts one message to one chat, chunked to the account's limit. It is
// how a scheduled job reaches a chat outside the normal turn.
func (b *Bot) SendTo(ctx context.Context, chatID, text string) error {
	chatID = NormalizeJID(chatID)
	b.mu.RLock()
	hook := b.sendHook
	b.mu.RUnlock()
	formatted := formatWhatsApp(text)
	if hook != nil {
		for _, chunk := range channel.Chunk(formatted, b.policySnapshot().EffectiveTextChunkLimit()) {
			hook(chatID, chunk)
		}
		return nil
	}
	if b.client == nil {
		return errors.New("no WhatsApp client available")
	}
	jid, err := types.ParseJID(chatID)
	if err != nil {
		return fmt.Errorf("bad chat id %q: %w", chatID, err)
	}
	for _, chunk := range channel.Chunk(formatted, b.policySnapshot().EffectiveTextChunkLimit()) {
		if _, err := b.client.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(chunk)}); err != nil {
			return err
		}
	}
	return nil
}

// SendFileTo uploads and sends an image or document to one WhatsApp chat.
func (b *Bot) SendFileTo(ctx context.Context, chatID, filename string, data []byte, caption string) error {
	chatID = NormalizeJID(chatID)
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "" || filename == "." || filename == "/" {
		filename = "attachment"
	}
	formattedCaption := formatWhatsApp(strings.TrimSpace(caption))
	b.mu.RLock()
	hook := b.sendFileHook
	b.mu.RUnlock()
	if hook != nil {
		hook(chatID, filename, data, formattedCaption)
		return nil
	}
	if b.client == nil {
		return errors.New("no WhatsApp client available")
	}
	jid, err := types.ParseJID(chatID)
	if err != nil {
		return fmt.Errorf("bad chat id %q: %w", chatID, err)
	}
	mimeType := detectMimeType(filename, data)
	if isImageExtension(filename) {
		up, err := b.client.Upload(ctx, data, whatsmeow.MediaImage)
		if err != nil {
			return fmt.Errorf("upload WhatsApp image: %w", err)
		}
		imgMsg := &waE2E.ImageMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
		}
		if formattedCaption != "" {
			imgMsg.Caption = proto.String(formattedCaption)
		}
		_, err = b.client.SendMessage(ctx, jid, &waE2E.Message{ImageMessage: imgMsg})
		return err
	}
	up, err := b.client.Upload(ctx, data, whatsmeow.MediaDocument)
	if err != nil {
		return fmt.Errorf("upload WhatsApp document: %w", err)
	}
	docMsg := &waE2E.DocumentMessage{
		URL:           proto.String(up.URL),
		DirectPath:    proto.String(up.DirectPath),
		MediaKey:      up.MediaKey,
		FileEncSHA256: up.FileEncSHA256,
		FileSHA256:    up.FileSHA256,
		FileLength:    proto.Uint64(up.FileLength),
		Mimetype:      proto.String(mimeType),
		FileName:      proto.String(filename),
		Title:         proto.String(filename),
	}
	if formattedCaption != "" {
		docMsg.Caption = proto.String(formattedCaption)
	}
	_, err = b.client.SendMessage(ctx, jid, &waE2E.Message{DocumentMessage: docMsg})
	return err
}

func detectMimeType(filename string, data []byte) string {
	if ext := strings.ToLower(filepath.Ext(filename)); ext != "" {
		if m := mime.TypeByExtension(ext); m != "" {
			if semi := strings.Index(m, ";"); semi > 0 {
				return strings.TrimSpace(m[:semi])
			}
			return m
		}
	}
	if len(data) > 0 {
		m := http.DetectContentType(data)
		if semi := strings.Index(m, ";"); semi > 0 {
			return strings.TrimSpace(m[:semi])
		}
		return m
	}
	return "application/octet-stream"
}

// reply sends one message. A failure is logged, never surfaced to the chat.
func (b *Bot) reply(ctx context.Context, chatID, text string) {
	b.mu.RLock()
	hook := b.sendHook
	b.mu.RUnlock()
	formatted := formatWhatsApp(text)
	if hook != nil {
		hook(chatID, formatted)
		return
	}
	if b.client == nil {
		b.logfLine("no client to reply: " + formatted)
		return
	}
	jid, err := types.ParseJID(chatID)
	if err != nil {
		b.logfLine("bad chat id " + chatID + ": " + err.Error())
		return
	}
	sendCtx := ctx
	if sendCtx == nil || sendCtx.Err() != nil {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
	}
	_, err = b.client.SendMessage(sendCtx, jid, &waE2E.Message{Conversation: proto.String(formatted)})
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
	return channel.NaturalError(err, lang...)
}

func unwrapMessage(msg *waE2E.Message) *waE2E.Message {
	if msg == nil {
		return nil
	}
	if ephem := msg.GetEphemeralMessage(); ephem != nil && ephem.GetMessage() != nil {
		return unwrapMessage(ephem.GetMessage())
	}
	if vo := msg.GetViewOnceMessage(); vo != nil && vo.GetMessage() != nil {
		return unwrapMessage(vo.GetMessage())
	}
	if vo2 := msg.GetViewOnceMessageV2(); vo2 != nil && vo2.GetMessage() != nil {
		return unwrapMessage(vo2.GetMessage())
	}
	if vo2ext := msg.GetViewOnceMessageV2Extension(); vo2ext != nil && vo2ext.GetMessage() != nil {
		return unwrapMessage(vo2ext.GetMessage())
	}
	if docCap := msg.GetDocumentWithCaptionMessage(); docCap != nil && docCap.GetMessage() != nil {
		return unwrapMessage(docCap.GetMessage())
	}
	return msg
}

func isImageExtension(name string) bool {
	return channel.IsImageExtension(name)
}

func isPDFVisionModel(modelID string) bool {
	return channel.IsPDFVisionModel(modelID)
}

func isAuthError(err error) bool {
	return channel.IsAuthError(err)
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
	case "cost":
		if inspector, ok := b.agent.(SessionInspector); ok {
			b.sendReply(ctx, request.ChatID, inspector.CostInfo(request, lang))
			return
		}
		b.sendReply(ctx, request.ChatID, b.agent.Status())
	case "plan":
		if inspector, ok := b.agent.(SessionInspector); ok {
			b.sendReply(ctx, request.ChatID, inspector.PlanInfo(request, lang))
			return
		}
		if lang == "id" {
			b.sendReply(ctx, request.ChatID, "Belum ada rencana (plan) aktif pada sesi ini.")
		} else {
			b.sendReply(ctx, request.ChatID, "No active plan in this session.")
		}
	case "compact":
		if inspector, ok := b.agent.(SessionInspector); ok {
			b.sendReply(ctx, request.ChatID, inspector.CompactSession(ctx, request, lang))
			return
		}
		if lang == "id" {
			b.sendReply(ctx, request.ChatID, "Fitur ringkas sesi tidak didukung oleh agent ini.")
		} else {
			b.sendReply(ctx, request.ChatID, "Session compaction is not supported by this agent.")
		}
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
			"/cost - Lihat pemakaian token dan estimasi biaya sesi",
			"/plan - Lihat daftar rencana tugas (plan) pada sesi aktif",
			"/compact - Ringkas riwayat percakapan untuk menghemat konteks",
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
		"/cost - View session token usage and estimated cost",
		"/plan - View the active task plan in this session",
		"/compact - Condense earlier conversation turns to save context",
		"/new or /reset - Start a fresh conversation session",
		"/stop - Stop currently running process",
		"/diff - View git changes in workspace",
		"/help or /menu - Show this help menu",
	}, "\n")
}

func splitCommand(text string) (string, string) {
	return channel.SplitCommand(text)
}
