package telegram

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/version"
)

// Agent is the bridge the bot drives. The app implements it over the same
// runner and session the terminal uses. Every turn carries the channel Request
// so one app can serve many accounts: the request names the account, the chat
// and the sender the turn belongs to.
type Agent interface {
	// RunPrompt runs one turn, calling progress with short status lines.
	RunPrompt(ctx context.Context, request channel.Request, prompt string, progress func(string)) (string, error)
	// RunPromptWithImage runs one turn with an image attached for a vision model.
	// mediaType is a MIME type and data is base64.
	RunPromptWithImage(ctx context.Context, request channel.Request, prompt, mediaType, data string, progress func(string)) (string, error)
	// Stop cancels the running turn.
	Stop()
	// NewSession starts a fresh conversation for the request's chat.
	NewSession(request channel.Request)
	// Model returns the current model id.
	Model() string
	// SetModel switches model by id or search text and returns the new id.
	SetModel(query string) (string, error)
	// Models returns the formatted list of available models.
	Models() string
	// Status returns a short multi-line status block.
	Status() string
}

// DiffProvider is an optional interface an Agent can implement to surface git diffs.
type DiffProvider interface {
	GitDiff(ctx context.Context) (string, error)
}

// AudioTranscriber can transcribe voice notes and audio to text.
type AudioTranscriber interface {
	TranscribeAudio(ctx context.Context, data []byte, filename string) (string, error)
}

// WorkspaceProvider gives access to the workspace directory path.
type WorkspaceProvider interface {
	Workspace() string
}

// VoicePromptRunner can run a turn initiated from a voice note, with fallback to a voice model.
type VoicePromptRunner interface {
	RunVoicePrompt(ctx context.Context, request channel.Request, prompt string, progress func(string)) (string, error)
}

// ProviderLister can return the providers currently active or usable.
type ProviderLister interface {
	ActiveProviders() []provider.Provider
}

// SessionInspector is an optional interface that exposes per-chat session cost, plan, and compaction.
type SessionInspector interface {
	CostInfo(request channel.Request, lang string) string
	PlanInfo(request channel.Request, lang string) string
	CompactSession(ctx context.Context, request channel.Request, lang string) string
}

// PendingApproval tracks one in-flight approval request waiting for a callback query.
type PendingApproval struct {
	ID       string
	Tool     string
	Detail   string
	Risk     string
	ChatID   int64
	MsgID    int64
	Decision chan string
}

// Bot long-polls Telegram and forwards operator messages to the agent.
type Bot struct {
	client *Client
	agent  Agent

	// pairMu guards the pairing state. Every update handler runs on its own
	// goroutine while the app mirrors a new pairing onto the running bot, so an
	// unguarded field here is read by one goroutine as another writes it, which
	// is a data race the detector reports. The state is reached only through
	// the methods below, so an unguarded access no longer compiles.
	pairMu      sync.RWMutex
	chatID      int64
	ownerUserID int64
	pairingCode string

	// OnPaired persists the pairing. It runs on the handler goroutine.
	OnPaired func(chatID, ownerUserID int64)
	// Log receives diagnostic lines; nil discards them.
	Log func(string)

	// identityMu guards the bot identity and the account policy, which are
	// written once before Run and read on every handler goroutine.
	identityMu sync.RWMutex
	// accountName labels the account this bot serves.
	accountName string
	// policy is the access policy of this account: who may talk to it.
	policy config.ChannelAccount
	// pairing collects unknown DM senders when the policy is "pairing".
	pairing *channel.PairingStore
	// selfID and username identify the bot itself, which is what a group
	// mention is matched against.
	selfID   int64
	username string
	// limiter bounds how fast one guest sender may post.
	limiter *channel.RateLimiter

	offset int64
	// cursorPath, when set, is where the last confirmed update id is kept so a
	// restart resumes instead of replaying the prompts Telegram still holds.
	cursorPath string

	// offsetMu guards offset, persisted and inflight. offset is the poll
	// cursor, advanced when an update is dispatched. persisted is the
	// contiguous watermark written to disk, advanced only once every earlier
	// update has finished, so a crash replays an in-flight prompt instead of
	// dropping it. inflight is the set of dispatched updates not yet done.
	offsetMu  sync.Mutex
	persisted int64
	inflight  map[int64]bool

	// handlers tracks in-flight update handlers so shutdown waits for them
	// instead of leaving a goroutine writing to a dead client.
	handlers sync.WaitGroup
	// runMu guards the single agent turn this bot may start. Commands that do
	// not run the agent are deliberately outside it, so /stop and /status are
	// answered while a turn is in flight.
	runMu   sync.Mutex
	running bool

	approvalsMu sync.Mutex
	approvals   map[string]*PendingApproval
	nextApprID  int64
}

// New builds a bot for a token and agent.
func New(token string, agent Agent) *Bot {
	return &Bot{
		client: NewClient(token),
		agent:  agent,
		// A bot with no policy would refuse every message, so the default is
		// the safe one: the operator's chat, allowlisted DMs, mention-only
		// groups.
		policy: config.ChannelAccount{Enabled: true, DMPolicy: config.DMPolicyAllowlist, GroupPolicy: config.GroupPolicyMention},
	}
}

// SetAccount installs the account identity and access policy. It is called
// before Run, so the handlers that read them see one value for the bot's life.
func (b *Bot) SetAccount(name string, policy config.ChannelAccount, pairing *channel.PairingStore) {
	b.identityMu.Lock()
	b.accountName = strings.TrimSpace(name)
	b.policy = policy
	b.pairing = pairing
	b.limiter = channel.ForAccount(policy)
	b.identityMu.Unlock()
}

// Account returns the account name this bot serves.
func (b *Bot) Account() string {
	b.identityMu.RLock()
	defer b.identityMu.RUnlock()
	return b.accountName
}

// Policy returns the access policy of this account.
func (b *Bot) Policy() config.ChannelAccount {
	b.identityMu.RLock()
	defer b.identityMu.RUnlock()
	return b.policy
}

// setIdentity records who the bot is, which is what a mention is matched
// against.
func (b *Bot) setIdentity(self User) {
	b.identityMu.Lock()
	b.selfID = self.ID
	b.username = strings.TrimSpace(strings.TrimPrefix(self.Username, "@"))
	b.identityMu.Unlock()
}

// Paired reports whether the bot is bound to a chat.
func (b *Bot) Paired() bool {
	b.pairMu.RLock()
	defer b.pairMu.RUnlock()
	return b.chatID != 0
}

// Pairing reports the paired chat and owner. A chat of 0 means unpaired.
func (b *Bot) Pairing() (chatID, ownerUserID int64) {
	b.pairMu.RLock()
	defer b.pairMu.RUnlock()
	return b.chatID, b.ownerUserID
}

// Pair binds the bot to a chat and its owner.
func (b *Bot) Pair(chatID, ownerUserID int64) {
	b.pairMu.Lock()
	b.chatID = chatID
	b.ownerUserID = ownerUserID
	b.pairMu.Unlock()
}

// Unpair drops the pairing.
func (b *Bot) Unpair() {
	b.Pair(0, 0)
}

// PairingCode returns the code a /pair message must present.
func (b *Bot) PairingCode() string {
	b.pairMu.RLock()
	defer b.pairMu.RUnlock()
	return b.pairingCode
}

// SetPairingCode records the active pairing code.
func (b *Bot) SetPairingCode(code string) {
	b.pairMu.Lock()
	b.pairingCode = code
	b.pairMu.Unlock()
}

// tryStartRun reserves the single agent turn, reporting false when one is
// already in flight from this bot.
func (b *Bot) tryStartRun() bool {
	b.runMu.Lock()
	defer b.runMu.Unlock()
	if b.running {
		return false
	}
	b.running = true
	return true
}

func (b *Bot) endRun() {
	b.runMu.Lock()
	b.running = false
	b.runMu.Unlock()
}

func (b *Bot) logf(format string, args ...any) {
	if b.Log != nil {
		b.Log(fmt.Sprintf(format, args...))
	}
}

// SetCursorPath records where the confirmed update id is persisted. The file is
// scoped to one bot by the caller, so two bots never share a cursor.
func (b *Bot) SetCursorPath(path string) { b.cursorPath = path }

// loadOffset reads the last confirmed update id. A missing or unreadable file
// yields zero, which is the current behaviour, so a broken cursor never wedges
// the loop on a stale offset.
func (b *Bot) loadOffset() int64 {
	if b.cursorPath == "" {
		return 0
	}
	data, err := os.ReadFile(b.cursorPath)
	if err != nil {
		return 0
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

// saveOffset persists the confirmed update id through a temporary file, so an
// interrupted write leaves the old value rather than a half-written one.
func (b *Bot) saveOffset(offset int64) {
	if b.cursorPath == "" {
		return
	}
	tmp := b.cursorPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(offset, 10)), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, b.cursorPath)
}

// Verify checks the token and returns the bot identity, which is also kept so
// a group mention can be recognised.
func (b *Bot) Verify(ctx context.Context) (User, error) {
	user, err := b.client.GetMe(ctx)
	if err == nil {
		b.setIdentity(user)
	}
	return user, err
}

// Run polls until the context is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	// Handlers may outlive a poll iteration; wait for them on the way out so
	// a stopping bot does not keep working in the background.
	defer b.handlers.Wait()

	// Resume from the last confirmed update so a restart does not replay the
	// prompts Telegram still holds.
	b.offsetMu.Lock()
	if b.offset == 0 {
		b.offset = b.loadOffset()
	}
	b.persisted = b.offset
	if b.inflight == nil {
		b.inflight = map[int64]bool{}
	}
	b.offsetMu.Unlock()

	commands := []BotCommand{
		{Command: "run", Description: "Run a prompt with live progress"},
		{Command: "diff", Description: "Show current git diff or file changes"},
		{Command: "cost", Description: "Show session token usage and cost"},
		{Command: "plan", Description: "Show active task plan"},
		{Command: "compact", Description: "Condense conversation history"},
		{Command: "stop", Description: "Stop the running turn"},
		{Command: "new", Description: "Start a new session"},
		{Command: "status", Description: "Show status"},
		{Command: "model", Description: "Choose or switch the model"},
		{Command: "models", Description: "List registered and available models"},
		{Command: "unpair", Description: "Detach this chat"},
		{Command: "help", Description: "List commands"},
	}
	if err := b.client.SetMyCommands(ctx, commands); err != nil {
		b.logf("could not publish the command menu: %v", err)
	}

	backoff := 3 * time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		b.offsetMu.Lock()
		offset := b.offset
		b.offsetMu.Unlock()
		updates, err := b.client.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			backoff = b.handlePollError(ctx, err, backoff)
			continue
		}
		backoff = 3 * time.Second
		for _, update := range updates {
			// Telegram returns updates from the current offset onwards, so an
			// id below it has already been handled. It must be skipped rather
			// than dispatched: running it again would repeat the command.
			// The offset is "the first update to return", not "the last one
			// seen", which is why the comparison is below rather than at.
			b.offsetMu.Lock()
			if update.UpdateID < b.offset {
				b.offsetMu.Unlock()
				continue
			}
			// Mark it in flight before the cursor moves past it, so the
			// persisted watermark can never step over an unfinished update.
			b.inflight[update.UpdateID] = true
			b.offset = update.UpdateID + 1
			b.offsetMu.Unlock()
			b.dispatch(ctx, update)
		}
	}
}

// dispatch runs one update handler on its own goroutine.
//
// This is what keeps the bot responsive: a prompt runs for minutes, and while
// it held the poll loop the operator could not send /stop, /status or a
// correction, which is precisely when those are needed.
func (b *Bot) dispatch(ctx context.Context, update Update) {
	b.handlers.Add(1)
	go func() {
		defer b.handlers.Done()
		defer b.finishUpdate(update.UpdateID)
		b.handleUpdate(ctx, update)
	}()
}

// finishUpdate marks an update done and persists the contiguous watermark: the
// highest offset below which every update has finished. Persisting only here,
// and not when the update is dispatched, means a crash during handling replays
// the prompt on the next start instead of losing it.
func (b *Bot) finishUpdate(updateID int64) {
	b.offsetMu.Lock()
	delete(b.inflight, updateID)
	for b.persisted < b.offset {
		if b.inflight[b.persisted] {
			break
		}
		b.persisted++
	}
	persisted := b.persisted
	b.offsetMu.Unlock()
	b.saveOffset(persisted)
}

// handlePollError backs off after a failed poll, honouring Telegram's own
// retry hint and treating a 409 (another poller holds the token) as fatal for
// this loop's cadence.
func (b *Bot) handlePollError(ctx context.Context, err error, backoff time.Duration) time.Duration {
	wait := backoff
	if retry := RetryAfterSeconds(err); retry > 0 {
		wait = time.Duration(retry) * time.Second
	}
	b.logf("poll failed: %v (retrying in %s)", err, wait)
	select {
	case <-ctx.Done():
	case <-time.After(wait):
	}
	next := backoff * 2
	if next > 60*time.Second {
		next = 60 * time.Second
	}
	return next
}

// handleUpdate dispatches one update. A failure is reported to the chat and
// never stops the loop.
func (b *Bot) handleUpdate(ctx context.Context, update Update) {
	defer func() {
		if recovered := recover(); recovered != nil {
			b.logf("recovered from panic while handling an update: %v", recovered)
		}
	}()
	switch {
	case update.CallbackQuery != nil:
		b.handleCallback(ctx, update.CallbackQuery)
	case update.Message != nil:
		b.handleMessage(ctx, update.Message)
	}
}

func (b *Bot) handleCallback(ctx context.Context, query *CallbackQuery) {
	chat := Chat{}
	if query.Message != nil {
		chat = query.Message.Chat
	}
	pairedChat, owner := b.Pairing()
	allowed := false
	if query.From != nil {
		if pairedChat != 0 && owner != 0 && query.From.ID == owner && (chat.ID == 0 || chat.ID == pairedChat) {
			allowed = true
		} else if channel.MatchesAny(b.Policy().Admins, strconv.FormatInt(query.From.ID, 10)) {
			allowed = true
		}
	}
	if !allowed {
		_ = b.client.AnswerCallbackQuery(ctx, query.ID, "Not allowed.")
		return
	}
	switch {
	case strings.HasPrefix(query.Data, "appr:"):
		b.handleApprovalCallback(ctx, query)
	case strings.HasPrefix(query.Data, modelProviderPrefix):
		b.showProviderModels(ctx, query, strings.TrimPrefix(query.Data, modelProviderPrefix))
	case strings.HasPrefix(query.Data, modelSelectPrefix):
		b.selectModel(ctx, query, strings.TrimPrefix(query.Data, modelSelectPrefix))
	default:
		_ = b.client.AnswerCallbackQuery(ctx, query.ID, "")
	}
}

// RequestApproval posts an interactive inline confirmation dialog and blocks
// until the operator selects Allow Once, Allow Always, or Deny (or timeout).
func (b *Bot) RequestApproval(ctx context.Context, tool, detail, risk string) (string, error) {
	chatID, _ := b.Pairing()
	if chatID == 0 {
		return "deny", errors.New("bot is not paired")
	}

	b.approvalsMu.Lock()
	if b.approvals == nil {
		b.approvals = make(map[string]*PendingApproval)
	}
	b.nextApprID++
	id := strconv.FormatInt(b.nextApprID, 10)
	ch := make(chan string, 1)
	approval := &PendingApproval{
		ID:       id,
		Tool:     tool,
		Detail:   detail,
		Risk:     risk,
		ChatID:   chatID,
		Decision: ch,
	}
	b.approvals[id] = approval
	b.approvalsMu.Unlock()

	defer func() {
		b.approvalsMu.Lock()
		delete(b.approvals, id)
		b.approvalsMu.Unlock()
	}()

	keyboard := &InlineKeyboard{
		InlineKeyboard: [][]InlineButton{
			{
				{Text: "Allow Once", CallbackData: "appr:once:" + id},
				{Text: "Always", CallbackData: "appr:always:" + id},
				{Text: "Deny", CallbackData: "appr:deny:" + id},
			},
		},
	}
	header := fmt.Sprintf("Approval required for %s", tool)
	if risk != "" {
		header = fmt.Sprintf("Approval required (%s) for %s", risk, tool)
	}
	text := header
	if strings.TrimSpace(detail) != "" {
		text += "\n\n" + detail
	}
	msg, err := b.client.SendMessage(ctx, chatID, text, keyboard)
	if err != nil {
		return "deny", err
	}
	approval.MsgID = msg.MessageID

	timer := time.NewTimer(3 * time.Minute)
	defer timer.Stop()

	select {
	case decision := <-ch:
		return decision, nil
	case <-timer.C:
		_ = b.client.EditMessageText(ctx, chatID, msg.MessageID, fmt.Sprintf("Approval for %s timed out (denied).", tool))
		return "deny", nil
	case <-ctx.Done():
		return "deny", ctx.Err()
	}
}

func (b *Bot) handleApprovalCallback(ctx context.Context, query *CallbackQuery) {
	parts := strings.Split(query.Data, ":")
	if len(parts) != 3 {
		_ = b.client.AnswerCallbackQuery(ctx, query.ID, "Invalid action.")
		return
	}
	action := parts[1] // once, always, deny
	id := parts[2]

	b.approvalsMu.Lock()
	appr, ok := b.approvals[id]
	b.approvalsMu.Unlock()

	if !ok {
		_ = b.client.AnswerCallbackQuery(ctx, query.ID, "Approval request expired or already answered.")
		return
	}

	_ = b.client.AnswerCallbackQuery(ctx, query.ID, "Recorded: "+action)
	statusText := fmt.Sprintf("Decision: %s for %s", action, appr.Tool)
	if query.Message != nil {
		_ = b.client.EditMessageTextWithKeyboard(ctx, query.Message.Chat.ID, query.Message.MessageID, statusText, &InlineKeyboard{InlineKeyboard: [][]InlineButton{}})
	}

	select {
	case appr.Decision <- action:
	default:
	}
}

func (b *Bot) handleMessage(ctx context.Context, message *Message) {
	chatID := message.Chat.ID
	text := strings.TrimSpace(message.Text)
	// A command is only a command when a slash introduces it (optionally preceded
	// by a group @mention). splitCommand happily reads the first word of any
	// message, so without this guard a plain sentence beginning with "run",
	// "new" or "stop" was routed as that command and either lost its first word
	// or reset the session.
	cmdText, isCommand := channel.ExtractCommandText(text)
	if isCommand {
		text = cmdText
	}

	request := b.requestFrom(message)
	policy := b.Policy()
	if !b.Paired() {
		// Unpaired: /pair is always accepted first, and if no explicit access
		// policy (admins, allowlist, open DM, or pairing DM store) is configured,
		// all other messages are refused with the pairing instruction.
		if isCommand {
			if command, argument := splitCommand(text); command == "pair" {
				b.tryPair(ctx, message, argument)
				return
			}
		}
		if !b.hasExplicitPolicy(policy, request) {
			b.reply(ctx, chatID, "This bot is not paired yet. Send /pair <code> with the code shown in Termixgo.")
			return
		}
	}

	decision := channel.Decide(policy, request)
	if b.isOperator(request) {
		// The paired owner is the account's implicit admin.
		decision = channel.Decision{Allow: true, Admin: true, Reason: "operator"}
	}
	if !decision.Allow {
		if b.mayPair(policy, request) {
			b.reply(ctx, chatID, b.pairingPrompt(request))
			return
		}
		// A refused message is ignored rather than answered: a stranger learns
		// nothing by probing, and a group message without a mention stays out
		// of everyone's way.
		b.logf("ignored a message (%s)", decision.Reason)
		return
	}

	lang := userLanguage(message)
	quoted := quotedText(message)
	request.Admin = decision.Admin
	if !decision.Admin {
		b.identityMu.RLock()
		limiter := b.limiter
		b.identityMu.RUnlock()
		if limiter != nil && !limiter.Allow(request.SenderID, time.Now()) {
			if message.From != nil && message.From.LanguageCode != "" && !strings.HasPrefix(strings.ToLower(message.From.LanguageCode), "id") {
				b.reply(ctx, chatID, "Too many messages. Please try again in a moment.")
			} else {
				b.reply(ctx, chatID, "Terlalu banyak pesan. Coba lagi sebentar ya.")
			}
			return
		}
		b.handleGuestMessage(ctx, request, message, text, isCommand)
		return
	}

	// A photo is a vision prompt: the caption is the instruction, or a default
	// asks the model to describe it. It runs before the command switch because
	// an image message carries no text.
	if len(message.Photo) > 0 {
		b.runPhoto(ctx, chatID, request, message)
		return
	}

	// Voice notes and audio tracks are transcribed and run as prompts.
	if message.Voice != nil {
		b.runVoice(ctx, chatID, request, message.Voice.FileID, "voice.ogg", channel.FormatQuotedPrompt(quoted, message.Caption))
		return
	}
	if message.Audio != nil {
		name := message.Audio.FileName
		if name == "" {
			name = "audio.mp3"
		}
		b.runVoice(ctx, chatID, request, message.Audio.FileID, name, channel.FormatQuotedPrompt(quoted, message.Caption))
		return
	}

	// Documents (files, patches, logs, images sent as documents) are saved or inspected.
	if message.Document != nil {
		b.runDocument(ctx, chatID, request, message)
		return
	}

	// No text means there is nothing to ask.
	if text == "" {
		return
	}

	if !isCommand {
		// Plain text is a prompt, which is the natural way to use the bot.
		b.runPrompt(ctx, chatID, request, channel.FormatQuotedPrompt(quoted, text))
		return
	}

	command, argument := splitCommand(text)
	switch command {
	case "help", "start", "menu":
		b.reply(ctx, chatID, helpText())
	case "status":
		b.reply(ctx, chatID, b.agent.Status())
	case "cost":
		if inspector, ok := b.agent.(SessionInspector); ok {
			b.reply(ctx, chatID, inspector.CostInfo(request, lang))
			return
		}
		b.reply(ctx, chatID, b.agent.Status())
	case "plan":
		if inspector, ok := b.agent.(SessionInspector); ok {
			b.reply(ctx, chatID, inspector.PlanInfo(request, lang))
			return
		}
		if lang == "id" {
			b.reply(ctx, chatID, "Belum ada rencana (plan) aktif pada sesi ini.")
		} else {
			b.reply(ctx, chatID, "No active plan in this session.")
		}
	case "compact":
		if inspector, ok := b.agent.(SessionInspector); ok {
			b.reply(ctx, chatID, inspector.CompactSession(ctx, request, lang))
			return
		}
		if lang == "id" {
			b.reply(ctx, chatID, "Fitur ringkas sesi tidak didukung oleh agent ini.")
		} else {
			b.reply(ctx, chatID, "Session compaction is not supported by this agent.")
		}
	case "pair":
		b.reply(ctx, chatID, "Already paired. Use /unpair first to move to another chat.")
	case "unpair":
		b.Unpair()
		if b.OnPaired != nil {
			b.OnPaired(0, 0)
		}
		b.reply(ctx, chatID, "Unpaired.")
	case "new", "reset", "clear":
		b.agent.NewSession(request)
		b.reply(ctx, chatID, "Started a new session.")
	case "stop":
		b.agent.Stop()
		b.reply(ctx, chatID, "Stopping the running turn.")
	case "model":
		if strings.TrimSpace(argument) == "" {
			// Open the picker rather than only reporting the active model: the
			// operator asked to choose one.
			b.sendModelMenu(ctx, chatID)
			return
		}
		updated, err := b.agent.SetModel(argument)
		if err != nil {
			b.reply(ctx, chatID, err.Error())
			return
		}
		b.reply(ctx, chatID, "Model is now "+updated+".")
	case "models":
		if lister, ok := b.agent.(interface{ ModelsFor(string) string }); ok {
			b.reply(ctx, chatID, lister.ModelsFor(lang))
		} else {
			b.reply(ctx, chatID, b.agent.Models())
		}
	case "diff":
		diffProv, ok := b.agent.(DiffProvider)
		if !ok {
			b.reply(ctx, chatID, "Diff is not supported by this agent.")
			return
		}
		diff, err := diffProv.GitDiff(ctx)
		if err != nil {
			b.reply(ctx, chatID, "git diff error: "+err.Error())
			return
		}
		trimmed := strings.TrimSpace(diff)
		if trimmed == "" {
			b.reply(ctx, chatID, "No git changes in the workspace.")
			return
		}
		if len(trimmed) > 3000 {
			_, sendErr := b.client.SendDocument(ctx, chatID, "changes.diff", []byte(trimmed), "Current git diff")
			if sendErr != nil {
				b.reply(ctx, chatID, "Could not send diff file: "+sendErr.Error())
			}
			return
		}
		b.reply(ctx, chatID, "```diff\n"+trimmed+"\n```")
	case "run", "query":
		prompt := strings.TrimSpace(argument)
		if prompt == "" {
			b.reply(ctx, chatID, "Usage: /run <prompt>")
			return
		}
		b.runPrompt(ctx, chatID, request, channel.FormatQuotedPrompt(quoted, prompt))
	default:
		b.reply(ctx, chatID, "Unknown command. Send /help.")
	}
}

func userLanguage(message *Message) string {
	if message != nil && message.From != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(message.From.LanguageCode)), "id") {
		return "id"
	}
	return "en"
}

func quotedText(message *Message) string {
	if message == nil || message.ReplyToMessage == nil {
		return ""
	}
	if text := strings.TrimSpace(message.ReplyToMessage.Text); text != "" {
		return text
	}
	return strings.TrimSpace(message.ReplyToMessage.Caption)
}

func (b *Bot) hasExplicitPolicy(policy config.ChannelAccount, request channel.Request) bool {
	if len(policy.Admins) > 0 || len(policy.AllowFrom) > 0 {
		return true
	}
	dm := config.NormalizeDMPolicy(policy.DMPolicy)
	if dm == config.DMPolicyOpen {
		return true
	}
	if dm == config.DMPolicyPairing && b.mayPair(policy, request) {
		return true
	}
	return false
}

// runPrompt runs one text turn and mirrors progress into a single edited card.
func (b *Bot) runPrompt(ctx context.Context, chatID int64, request channel.Request, prompt string) {
	b.runWithCard(ctx, chatID, func(runCtx context.Context, progress func(string)) (string, error) {
		return b.agent.RunPrompt(runCtx, request, prompt, progress)
	})
}

// runPhoto downloads a photo the operator sent and runs the agent with it
// attached, so a vision model can see it. The message caption is the prompt.
func (b *Bot) runPhoto(ctx context.Context, chatID int64, request channel.Request, message *Message) {
	prompt := strings.TrimSpace(message.Caption)
	if prompt == "" {
		prompt = "Describe this image and act on anything notable in it."
	}
	prompt = channel.FormatQuotedPrompt(quotedText(message), prompt)
	// Telegram sends several sizes; the last is the largest.
	photo := message.Photo[len(message.Photo)-1]
	file, err := b.client.GetFile(ctx, photo.FileID)
	if err != nil {
		b.reply(ctx, chatID, "Could not fetch the image.")
		return
	}
	data, err := b.client.DownloadFile(ctx, file.FilePath)
	if err != nil {
		b.reply(ctx, chatID, "Could not download the image.")
		return
	}
	if wp, ok := b.agent.(WorkspaceProvider); ok && wp.Workspace() != "" {
		ext := filepath.Ext(file.FilePath)
		if ext == "" {
			ext = ".jpg"
		}
		if savedName, rel, saved := channel.SaveUpload(wp.Workspace(), fmt.Sprintf("tg_%d%s", time.Now().UnixNano(), ext), data); saved {
			prompt = fmt.Sprintf("[Image uploaded: %s saved to %s]\n\n%s", savedName, rel, prompt)
		}
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	mediaType := imageMediaType(file.FilePath)
	b.runWithCard(ctx, chatID, func(runCtx context.Context, progress func(string)) (string, error) {
		return b.agent.RunPromptWithImage(runCtx, request, prompt, mediaType, encoded, progress)
	})
}

func isImageExtension(name string) bool {
	return channel.IsImageExtension(name)
}

func isPDFVisionModel(modelID string) bool {
	return channel.IsPDFVisionModel(modelID)
}

// runDocument downloads a document file. If it is an image, it is passed to
// vision; otherwise text files are displayed inline and all files are saved
// to the workspace uploads directory when available.
func (b *Bot) runDocument(ctx context.Context, chatID int64, request channel.Request, message *Message) {
	doc := message.Document
	if doc == nil {
		return
	}
	prompt := strings.TrimSpace(message.Caption)
	quoted := quotedText(message)

	file, err := b.client.GetFile(ctx, doc.FileID)
	if err != nil {
		b.reply(ctx, chatID, "Could not fetch document.")
		return
	}
	data, err := b.client.DownloadFile(ctx, file.FilePath)
	if err != nil {
		b.reply(ctx, chatID, "Could not download document.")
		return
	}

	if strings.HasPrefix(strings.ToLower(doc.MimeType), "image/") || isImageExtension(doc.FileName) {
		if prompt == "" {
			prompt = "Describe this image and act on anything notable in it."
		}
		prompt = channel.FormatQuotedPrompt(quoted, prompt)
		if wp, ok := b.agent.(WorkspaceProvider); ok && wp.Workspace() != "" {
			imgName := filepath.Base(doc.FileName)
			if imgName == "." || imgName == "/" || imgName == "" {
				imgName = fmt.Sprintf("tg_%d.jpg", time.Now().UnixNano())
			}
			if savedName, rel, saved := channel.SaveUpload(wp.Workspace(), imgName, data); saved {
				prompt = fmt.Sprintf("[Image uploaded: %s saved to %s]\n\n%s", savedName, rel, prompt)
			}
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		mediaType := doc.MimeType
		if mediaType == "" {
			mediaType = imageMediaType(doc.FileName)
		}
		b.runWithCard(ctx, chatID, func(runCtx context.Context, progress func(string)) (string, error) {
			return b.agent.RunPromptWithImage(runCtx, request, prompt, mediaType, encoded, progress)
		})
		return
	}

	fileName := filepath.Base(doc.FileName)
	if fileName == "." || fileName == "/" || fileName == "" {
		fileName = "uploaded_document"
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

	isPDF := strings.ToLower(doc.MimeType) == "application/pdf" || strings.HasSuffix(strings.ToLower(doc.FileName), ".pdf")
	if isPDF && isPDFVisionModel(b.agent.Model()) {
		if prompt != "" {
			builder.WriteString(channel.FormatQuotedPrompt(quoted, prompt))
		} else {
			builder.WriteString(channel.FormatQuotedPrompt(quoted, "Please inspect and analyze this uploaded PDF document."))
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		b.runWithCard(ctx, chatID, func(runCtx context.Context, progress func(string)) (string, error) {
			return b.agent.RunPromptWithImage(runCtx, request, builder.String(), "application/pdf", encoded, progress)
		})
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
		builder.WriteString(channel.FormatQuotedPrompt(quoted, "Please inspect the uploaded document and proceed."))
	}

	b.runPrompt(ctx, chatID, request, builder.String())
}

// runVoice downloads an incoming voice note or audio file, transcribes it to
// text, and runs the agent with the transcribed prompt.
func (b *Bot) runVoice(ctx context.Context, chatID int64, request channel.Request, fileID, filename, caption string) {
	transcriber, ok := b.agent.(AudioTranscriber)
	if !ok {
		b.reply(ctx, chatID, "Voice transcription is not supported by this agent.")
		return
	}

	file, err := b.client.GetFile(ctx, fileID)
	if err != nil {
		b.reply(ctx, chatID, "Could not fetch voice message: "+err.Error())
		return
	}
	data, err := b.client.DownloadFile(ctx, file.FilePath)
	if err != nil {
		b.reply(ctx, chatID, "Could not download voice message: "+err.Error())
		return
	}

	text, err := transcriber.TranscribeAudio(ctx, data, filename)
	if err != nil {
		b.reply(ctx, chatID, "Transcription failed: "+err.Error())
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		b.reply(ctx, chatID, "No speech detected in audio.")
		return
	}

	b.reply(ctx, chatID, "Transcribed: <i>\""+escapeHTML(text)+"\"</i>")

	prompt := text
	if caption = strings.TrimSpace(caption); caption != "" {
		prompt = caption + "\n\n" + text
	}

	if runner, ok := b.agent.(VoicePromptRunner); ok {
		b.runWithCard(ctx, chatID, func(runCtx context.Context, progress func(string)) (string, error) {
			return runner.RunVoicePrompt(runCtx, request, prompt, progress)
		})
		return
	}

	b.runPrompt(ctx, chatID, request, prompt)
}

// typingRefresh is how often the typing action is renewed while a turn runs.
// Telegram clears the indicator after about five seconds, so four keeps it
// continuous without sending a request the API would ignore anyway. It is a
// variable so a test can exercise the keepalive without waiting out the real
// interval.
var typingRefresh = 4 * time.Second

// keepTyping renews the chat's typing indicator until done is closed or the
// bot's context ends. It runs off the run's own goroutine so a slow network
// call cannot delay the turn.
func (b *Bot) keepTyping(ctx context.Context, chatID int64, done <-chan struct{}) {
	ticker := time.NewTicker(typingRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = b.client.SendChatAction(ctx, chatID, "typing")
		}
	}
}

func formatRunFailure(err error) string {
	redacted := channel.Redact(err.Error(), nil)
	if channel.IsAuthError(err) || errors.Is(err, channel.ErrBusy) || errors.Is(err, context.Canceled) {
		return "Run failed: " + channel.NaturalError(err, "en") + " (" + redacted + ")"
	}
	return "Run failed: " + redacted
}

// runWithCard runs one turn behind the single-run gate and mirrors progress
// into one message that is edited in place instead of flooding the chat.
func (b *Bot) runWithCard(ctx context.Context, chatID int64, run func(context.Context, func(string)) (string, error)) {
	if !b.tryStartRun() {
		b.reply(ctx, chatID, "A run is already in progress. Send /stop to stop it, then retry.")
		return
	}
	defer b.endRun()

	statusMessage, err := b.client.SendMessage(ctx, chatID, "Working...", nil)
	if err != nil {
		b.logf("could not send the progress message: %v", err)
	}
	_ = b.client.SendChatAction(ctx, chatID, "typing")
	// Telegram clears a typing action after a few seconds, so a long turn
	// showed no sign of life for most of its run and read as stuck. The
	// keepalive renews the indicator until the turn returns, which is what
	// tells the operator the agent is still working.
	typingDone := make(chan struct{})
	go b.keepTyping(ctx, chatID, typingDone)
	defer close(typingDone)

	// The card grows with the turn instead of being replaced by the newest line.
	// Replacing lost every earlier step: the operator saw one tool at a time and
	// the task itself scrolled away. An edit the throttle skips is not lost
	// either, because the next edit carries the whole list.
	var lines []string
	var lastEdit time.Time
	flush := func(force bool) {
		if statusMessage.MessageID == 0 {
			return
		}
		// Throttle edits: Telegram rate-limits rapid edits to one message.
		if !force && time.Since(lastEdit) < 2*time.Second {
			return
		}
		lastEdit = time.Now()
		editCtx := ctx
		if editCtx == nil || editCtx.Err() != nil {
			var cancel context.CancelFunc
			editCtx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
		}
		_ = b.client.EditMarkdown(editCtx, chatID, statusMessage.MessageID, strings.Join(lines, "\n"), nil)
	}
	progress := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		if len(lines) > 0 && lines[len(lines)-1] == line {
			return
		}
		lines = append(lines, line)
		lines = capCardLines(lines, telegramChunkLimit)
		flush(false)
	}

	answer, runErr := run(ctx, progress)

	if statusMessage.MessageID == 0 {
		// No card to edit: deliver the failure or the answer directly.
		if runErr != nil {
			b.replyMarkdown(ctx, chatID, channel.Redact(formatRunFailure(runErr), nil))
			return
		}
		if strings.TrimSpace(answer) != "" && channel.Silent(answer) {
			return
		}
		if strings.TrimSpace(answer) == "" {
			answer = "(no output)"
		}
		b.replyMarkdown(ctx, chatID, answer)
		return
	}

	if runErr != nil {
		progress(formatRunFailure(runErr))
		flush(true)
		return
	}
	if strings.TrimSpace(answer) != "" && channel.Silent(answer) {
		_ = b.client.DeleteMessage(ctx, chatID, statusMessage.MessageID)
		return
	}
	if strings.TrimSpace(answer) == "" {
		answer = "(no output)"
	}

	// The card carried the tool activity while the turn ran. Once the turn is
	// done that activity has served its purpose, so the card is replaced by the
	// answer alone: the chat is left with the conversation (the prompt, the
	// agent's reply) instead of the backend steps behind it. When the answer is
	// too long for one message the card carries its first part and the rest
	// follow as new messages, because an edit past the limit is truncated.
	first, rest := nextChunk(answer, telegramChunkLimit)
	if err := b.client.EditMarkdown(ctx, chatID, statusMessage.MessageID, first, nil); err != nil {
		// The card could not be rewritten, so deliver the whole answer as its
		// own message rather than lose it.
		b.replyMarkdown(ctx, chatID, answer)
		return
	}
	if rest != "" {
		b.replyMarkdown(ctx, chatID, rest)
	}
}

// telegramChunkLimit is the body size the bot targets, under Telegram's 4096
// byte cap so a converted Markdown body never trips it.
const telegramChunkLimit = 3800

// replyMarkdown sends Markdown text, split into Telegram-sized messages so a
// long answer arrives whole instead of being truncated at the cap.
//
// Each chunk is split on raw Markdown and sized so its converted Telegram HTML
// never exceeds telegramChunkLimit, keeping tags and HTML entities balanced in
// every message.
func (b *Bot) replyMarkdown(ctx context.Context, chatID int64, text string) {
	text = channel.Redact(text, nil)
	sendCtx := ctx
	if sendCtx == nil || sendCtx.Err() != nil {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
	}
	for len(text) > 0 {
		part, rest := nextChunk(text, telegramChunkLimit)
		text = rest
		if _, err := b.client.SendMarkdown(sendCtx, chatID, part, nil); err != nil {
			b.logf("send failed: %v", err)
		}
	}
}

// nextChunk splits text into a part under limit bytes (both raw and after
// Telegram HTML conversion) and the remainder. The cut lands on a rune
// boundary, which Telegram requires, and prefers a line boundary near the limit
// so a sentence is not split mid-line when a newline is close. The newline
// itself stays with the next part: the renderer trims a trailing newline, so
// keeping it here would drop it.
func nextChunk(text string, limit int) (string, string) {
	if len(text) <= limit && (limit < 64 || len(markdownToTelegramHTML(text)) <= limit) {
		return text, ""
	}
	capBytes := limit
	if capBytes > len(text) {
		capBytes = len(text)
	}
	for {
		cut := capBytes
		for cut > 0 && cut < len(text) && !utf8.RuneStart(text[cut]) {
			cut--
		}
		if index := strings.LastIndexByte(text[:cut], '\n'); index > 0 && index > cut/2 {
			cut = index
		}
		if cut <= 0 {
			_, width := utf8.DecodeRuneInString(text)
			if width <= 0 {
				width = 1
			}
			return text[:width], text[width:]
		}
		if limit < 64 || len(markdownToTelegramHTML(text[:cut])) <= limit || cut <= 16 {
			return text[:cut], text[cut:]
		}
		capBytes = (cut * 3) / 4
		if capBytes < 16 {
			capBytes = 16
		}
	}
}

// capCardLines keeps the newest lines within limit bytes, dropping the oldest so
// the growing card never exceeds the Telegram message limit.
func capCardLines(lines []string, limit int) []string {
	total := 0
	for _, line := range lines {
		total += len(line) + 1
	}
	if len(lines) > 0 {
		total--
	}
	kept := lines
	for len(kept) > 1 && total > limit {
		total -= len(kept[0]) + 1
		kept = kept[1:]
	}
	return kept
}

// imageMediaType maps a Telegram file path to a media type; photos it sends are
// jpeg, but an uploaded png or webp keeps its own suffix.
func imageMediaType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

func (b *Bot) tryPair(ctx context.Context, message *Message, code string) {
	expected := b.PairingCode()
	if strings.TrimSpace(expected) == "" {
		b.reply(ctx, message.Chat.ID, "No pairing code is active. Run /setup in Termixgo.")
		return
	}
	if strings.TrimSpace(code) != strings.TrimSpace(expected) {
		b.reply(ctx, message.Chat.ID, "Wrong pairing code.")
		return
	}
	ownerUserID := int64(0)
	if message.From != nil {
		ownerUserID = message.From.ID
	}
	b.Pair(message.Chat.ID, ownerUserID)
	if b.OnPaired != nil {
		b.OnPaired(message.Chat.ID, ownerUserID)
	}
	b.reply(ctx, message.Chat.ID, fmt.Sprintf("Paired. Send any message to run the agent.\n%s", helpText()))
}

// requestFrom normalizes one Telegram message into the transport-neutral
// request the gate and the app read.
func (b *Bot) requestFrom(message *Message) channel.Request {
	request := channel.Request{
		Channel: config.ChannelTelegram,
		Account: b.Account(),
		ChatID:  strconv.FormatInt(message.Chat.ID, 10),
		IsGroup: !message.Chat.IsPrivate(),
	}
	if message.From != nil {
		request.SenderID = strconv.FormatInt(message.From.ID, 10)
		request.SenderName = strings.TrimSpace(message.From.Username)
	}
	if request.SenderName == "" && message.From != nil && message.From.ID != 0 {
		request.SenderName = strconv.FormatInt(message.From.ID, 10)
	}
	request.MentionsBot = b.mentionsBot(message)
	return request
}

// mentionsBot reports whether a message addresses this bot.
//
// Telegram marks a mention as a text entity (the "@name" form) or, for a user
// with no username, as a text_mention entity carrying the id; a reply to one
// of the bot's own messages counts too. Both message text/entities and media
// caption/caption_entities are checked.
func (b *Bot) mentionsBot(message *Message) bool {
	if message == nil {
		return false
	}
	b.identityMu.RLock()
	selfID, username := b.selfID, b.username
	b.identityMu.RUnlock()

	if reply := message.ReplyToMessage; reply != nil && reply.From != nil && selfID != 0 && reply.From.ID == selfID {
		return true
	}
	if entitiesMentionBot(message.Text, message.Entities, selfID, username) {
		return true
	}
	if entitiesMentionBot(message.Caption, message.CaptionEntities, selfID, username) {
		return true
	}
	return false
}

func entitiesMentionBot(raw string, entities []MessageEntity, selfID int64, username string) bool {
	lowered := strings.ToLower(raw)
	for _, entity := range entities {
		switch entity.Type {
		case "mention":
			if username != "" && strings.Contains(lowered, "@"+strings.ToLower(username)) {
				return true
			}
		case "text_mention":
			if entity.User != nil && selfID != 0 && entity.User.ID == selfID {
				return true
			}
		}
	}
	return false
}

// isOperator reports whether a sender is the account's paired owner, who is an
// admin whether or not the policy lists one. The pairing binds one chat, so a
// message from the same user in another chat is not the operator: that is what
// stops an owner id from being reused elsewhere.
//
// A legacy pairing may carry no owner id (the chat was configured by hand).
// In that case the paired private chat is the operator's, which is the rule the
// pre-registry gate used.
func (b *Bot) isOperator(request channel.Request) bool {
	pairedChat, owner := b.Pairing()
	if pairedChat == 0 {
		return false
	}
	if strings.TrimSpace(request.ChatID) != strconv.FormatInt(pairedChat, 10) {
		return false
	}
	if owner == 0 {
		return !request.IsGroup
	}
	return strings.TrimSpace(request.SenderID) == strconv.FormatInt(owner, 10)
}

// mayPair reports whether an unknown DM sender should receive a pairing code.
func (b *Bot) mayPair(policy config.ChannelAccount, request channel.Request) bool {
	if request.IsGroup || config.NormalizeDMPolicy(policy.DMPolicy) != config.DMPolicyPairing {
		return false
	}
	b.identityMu.RLock()
	store := b.pairing
	b.identityMu.RUnlock()
	return store != nil
}

// pairingPrompt records the request and returns the message the sender reads.
// It is written like a person replying, because the sender is a person.
func (b *Bot) pairingPrompt(request channel.Request) string {
	b.identityMu.RLock()
	store := b.pairing
	b.identityMu.RUnlock()
	pending, created := store.Request(config.ChannelTelegram, request.Account, request.SenderID, request.SenderName)
	if created {
		b.logf("pairing request from %s (%s)", request.SenderID, pending.Code)
	}
	return "Halo! Percakapan ini belum diizinkan. Sandi pairing kamu: " + pending.Code +
		"\nMinta operator menyetujui dengan: termixgo channel pairing approve " + pending.Code
}

// handleGuestMessage serves a sender the policy allows but who is not an admin:
// text turns only, and a small set of commands.
func (b *Bot) handleGuestMessage(ctx context.Context, request channel.Request, message *Message, text string, isCommand bool) {
	chatID := message.Chat.ID
	if len(message.Photo) > 0 || message.Voice != nil || message.Audio != nil || message.Document != nil {
		b.reply(ctx, chatID, "Maaf, untuk sekarang saya hanya bisa membalas pesan teks di percakapan ini.")
		return
	}
	if text == "" {
		return
	}
	quoted := quotedText(message)
	if !isCommand {
		b.runPrompt(ctx, chatID, request, channel.FormatQuotedPrompt(quoted, text))
		return
	}
	command, rest := splitCommand(text)
	switch command {
	case "help", "start", "menu":
		b.reply(ctx, chatID, helpText())
	case "status":
		b.reply(ctx, chatID, "Sesi ini aktif. Silakan kirim pertanyaan.")
	case "new", "reset", "clear":
		b.agent.NewSession(request)
		b.reply(ctx, chatID, "Mulai sesi baru ya.")
	case "run", "query":
		prompt := strings.TrimSpace(rest)
		if prompt == "" {
			b.reply(ctx, chatID, "Usage: /run <prompt>")
			return
		}
		b.runPrompt(ctx, chatID, request, channel.FormatQuotedPrompt(quoted, prompt))
	default:
		b.reply(ctx, chatID, "Maaf, perintah itu hanya untuk operator.")
	}
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	// Error text is the one path where a raw URL (the Bot API client builds
	// URLs that carry the token) or a provider body (which echoes key
	// prefixes) could reach the chat unredacted: those strings never pass
	// through the answer redaction, so every line crosses RedactForChannel
	// on the way out.
	text = channel.Redact(text, nil)
	sendCtx := ctx
	if sendCtx == nil || sendCtx.Err() != nil {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
	}
	if _, err := b.client.SendMessage(sendCtx, chatID, text, nil); err != nil {
		b.logf("send failed: %v", err)
	}
}

// SendTo posts one message to one chat, chunked to the account's limit. It is
// how a scheduled job reaches a chat that is not the paired operator chat.
func (b *Bot) SendTo(ctx context.Context, chatID int64, text string) error {
	limit := b.Policy().EffectiveTextChunkLimit()
	for _, chunk := range channel.Chunk(text, limit) {
		if _, err := b.client.SendMarkdown(ctx, chatID, chunk, nil); err != nil {
			return err
		}
	}
	return nil
}

// SendFileTo uploads a document or image to one Telegram chat.
func (b *Bot) SendFileTo(ctx context.Context, chatID int64, filename string, data []byte, caption string) error {
	_, err := b.client.SendDocument(ctx, chatID, filename, data, caption)
	return err
}

// Send posts a message to the paired chat, whether or not the bot is polling.
// It is how a scheduled job reaches the operator outside a turn.
func (b *Bot) Send(ctx context.Context, text string) error {
	chatID, _ := b.Pairing()
	if chatID == 0 {
		return errors.New("the bot is not paired")
	}
	return b.SendTo(ctx, chatID, text)
}

// splitCommand parses "/name argument" or "name argument", tolerating the
// "@botname" suffix Telegram adds to group commands.
func splitCommand(text string) (string, string) {
	return channel.SplitCommand(text)
}

func helpText() string {
	return strings.Join([]string{
		version.Name + " companion bot",
		"",
		"Send any text to run it as a prompt.",
		"Send a photo (with a caption) to ask about an image.",
		"/run <prompt>  run with a live progress card",
		"/diff          show current git diff",
		"/cost          show session token usage and cost",
		"/plan          show active task plan",
		"/compact       condense conversation history",
		"/stop          stop the running turn",
		"/new           start a new session",
		"/model [id]    choose a model, or switch by id",
		"/models        list registered and available models",
		"/status        show status",
		"/unpair        detach this chat",
		"/help          this list",
	}, "\n")
}
