package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/agenthome"
	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
	"github.com/99apps-id/termixgo/internal/telegram"
	"github.com/99apps-id/termixgo/internal/whatsapp"
)

// ChannelStatus is one account's runtime state, for the status views.
type ChannelStatus struct {
	Channel string
	Account string
	Agent   string
	Enabled bool
	State   string
}

// ErrNoChannelsEnabled reports that the assistant started with no active channel accounts.
var ErrNoChannelsEnabled = errors.New("no channel accounts are enabled")

// channelRuntime is one running account.
type channelRuntime struct {
	id     channel.AccountID
	cancel context.CancelFunc
	bot    *telegram.Bot
	waBot  *whatsapp.Bot
	status string
	// send posts one message to one chat on this account, which is what a
	// scheduled job's explicit target uses.
	send     func(ctx context.Context, chatID, text string) error
	sendFile func(ctx context.Context, chatID, filename string, data []byte, caption string) error
}

// StartChannels starts every enabled account of every configured channel.
// A broken account is reported and skipped: one bad token must not stop the
// other agents from answering.
func (a *App) StartChannels() error {
	cfg := a.Config()
	var problems []string
	started := 0
	for _, name := range cfg.EnabledTelegramAccounts() {
		if err := a.StartChannel(config.ChannelTelegram, name); err != nil {
			problems = append(problems, fmt.Sprintf("telegram/%s: %v", name, err))
			continue
		}
		started++
	}
	for _, name := range cfg.EnabledWhatsAppAccounts() {
		if err := a.StartChannel(config.ChannelWhatsApp, name); err != nil {
			problems = append(problems, fmt.Sprintf("whatsapp/%s: %v", name, err))
			continue
		}
		started++
	}
	switch {
	case started == 0 && len(problems) == 0:
		return ErrNoChannelsEnabled
	case started == 0:
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// StartChannel starts one account.
func (a *App) StartChannel(kind, name string) error {
	kind = strings.ToLower(strings.TrimSpace(kind))
	name = strings.TrimSpace(name)
	if name == "" {
		name = config.FinalAgentName
	}
	key := channel.AccountID{Channel: kind, Account: name}

	a.mu.Lock()
	if _, running := a.channels[key]; running {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	switch kind {
	case config.ChannelTelegram:
		return a.startTelegramAccount(name)
	case config.ChannelWhatsApp:
		return a.startWhatsAppAccount(name)
	default:
		return fmt.Errorf("unknown channel %q", kind)
	}
}

// StopChannel stops one account, if it is running.
func (a *App) StopChannel(kind, name string) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	name = strings.TrimSpace(name)
	if name == "" {
		name = config.FinalAgentName
	}
	key := channel.AccountID{Channel: kind, Account: name}

	a.mu.Lock()
	runtime := a.channels[key]
	delete(a.channels, key)
	if kind == config.ChannelTelegram && name == config.FinalAgentName {
		a.bot = nil
		a.botCancel = nil
		a.botStatus = ""
	}
	a.mu.Unlock()

	if runtime != nil && runtime.cancel != nil {
		runtime.cancel()
	}
}

// StopChannels stops every running account.
func (a *App) StopChannels() {
	a.mu.Lock()
	keys := make([]channel.AccountID, 0, len(a.channels))
	for key := range a.channels {
		keys = append(keys, key)
	}
	outboxCancel := a.outboxCancel
	a.outboxCancel = nil
	a.mu.Unlock()
	if outboxCancel != nil {
		outboxCancel()
	}
	for _, key := range keys {
		a.StopChannel(key.Channel, key.Account)
	}
}

// ChannelStatuses lists every configured account with its runtime state.
func (a *App) ChannelStatuses() []ChannelStatus {
	cfg := a.Config()
	a.mu.Lock()
	runtimes := make(map[channel.AccountID]*channelRuntime, len(a.channels))
	for key, runtime := range a.channels {
		runtimes[key] = runtime
	}
	a.mu.Unlock()

	var statuses []ChannelStatus
	for name, entry := range cfg.Channels.Telegram.Accounts {
		key := channel.AccountID{Channel: config.ChannelTelegram, Account: name}
		state := "off"
		if !entry.Enabled {
			state = "disabled"
		} else if runtime, ok := runtimes[key]; ok {
			state = runtime.status
		} else if !a.store.Has(secrets.TelegramAccountTokenKey(name)) {
			state = "no token"
		} else if entry.ChatID != 0 {
			state = "paired, not running"
		} else {
			state = "not paired"
		}
		statuses = append(statuses, ChannelStatus{
			Channel: config.ChannelTelegram, Account: name,
			Agent: entry.AgentName(name), Enabled: entry.Enabled, State: state,
		})
	}
	for name, entry := range cfg.Channels.WhatsApp.Accounts {
		key := channel.AccountID{Channel: config.ChannelWhatsApp, Account: name}
		state := "linked, not running"
		if !entry.Enabled {
			state = "disabled"
		} else if runtime, ok := runtimes[key]; ok {
			state = runtime.status
		} else if agentName := entry.AgentName(name); !whatsAppLinked(a.Config(), agentName, name) {
			state = "not linked"
		}
		statuses = append(statuses, ChannelStatus{
			Channel: config.ChannelWhatsApp, Account: name,
			Agent: entry.AgentName(name), Enabled: entry.Enabled, State: state,
		})
	}
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].Channel != statuses[j].Channel {
			return statuses[i].Channel < statuses[j].Channel
		}
		return statuses[i].Account < statuses[j].Account
	})
	return statuses
}

// whatsAppLinked reports whether the device store file of one account exists.
// It reads the same path the login command writes, so the status view agrees
// with what `channel status` prints.
func whatsAppLinked(cfg config.Config, agentName, account string) bool {
	paths, err := agenthome.Resolve(agentName, cfg.Agent(agentName).Workspace)
	if err != nil {
		return false
	}
	path := whatsapp.SessionPath(paths.Home, account)
	if _, err := os.Stat(path); err != nil {
		return false
	}
	return true
}

// RedactForChannel removes secret-shaped text and every stored secret value
// from an answer before it leaves for a chat. The scan is the last line of
// defence: a model that echoes a key from a file must not leak it.
func (a *App) RedactForChannel(text string) string {
	known := []string{}
	if a.store != nil {
		for _, key := range a.store.Keys() {
			if value := a.store.Get(key); value != "" {
				known = append(known, value)
			}
		}
	}
	return channel.Redact(text, known)
}

// runChannelTurn runs one turn owned by a channel chat. The answer is what
// this turn produced, not the session's last answer: a model that said nothing
// must not make the bot repeat an earlier reply.
func (a *App) runChannelTurn(ctx context.Context, agentName string, session *agent.Session, interactor Interactor, prompt string, images []provider.Image, guest bool, maxSteps int, costBudgetUSD float64, progress func(string)) (string, error) {
	notice := ""
	if progress != nil {
		// The opening line is part of the contract: the caller shows it at
		// once, before the first event arrives.
		progress("Working...")
		claim := a.SetObserver(func(event agent.Event) {
			switch {
			case event.Kind == agent.EventNotice && strings.TrimSpace(event.Text) != "":
				notice = event.Text
			case event.Kind == agent.EventError && event.Err != nil:
				notice = event.Err.Error()
			}
			if line := telegramProgressLine(event); line != "" {
				progress(line)
			}
		})
		defer a.ClearObserver(claim)
	}
	options := runOptions{
		images:        images,
		session:       session,
		agentName:     agentName,
		interactor:    interactor,
		guest:         guest,
		maxSteps:      maxSteps,
		costBudgetUSD: costBudgetUSD,
	}
	mark := session.MessageCount()
	if err := a.runWithOptions(ctx, prompt, options); err != nil {
		return "", err
	}
	answer := strings.TrimSpace(session.LastAssistantTextSince(mark))

	// Strict grounding: an answer with no tool call behind it is challenged
	// once, and an answer that still has none is marked rather than passed off
	// as verified. A guest run is always strict.
	grounding := a.Config().AgentGrounding(agentName)
	if guest {
		grounding = config.GroundingStrict
	}
	if evidenceRetryNeeded(grounding, answer, session.ToolCallCountSince(mark)) {
		retryMark := session.MessageCount()
		if err := a.runWithOptions(ctx, prompt+"\n\n"+evidenceReminder, options); err == nil {
			if second := strings.TrimSpace(session.LastAssistantTextSince(retryMark)); second != "" {
				answer = second
			}
		}
		if session.ToolCallCountSince(retryMark) == 0 {
			answer += "\n\n" + unverifiedNote
		}
	}
	if answer != "" {
		return answer, nil
	}
	return notice, nil
}

// evidenceReminder is the one retry's instruction: demand evidence, and accept
// an honest "not found" as the answer.
const evidenceReminder = "Pengingat bukti: dukung setiap klaim faktual dengan hasil tool (sebut path dan baris, atau URL). Bila tidak ada bukti, katakan tidak ditemukan dan sebutkan apa yang sudah dicoba. Jangan mengarang."

// unverifiedNote marks an answer that stayed unsupported after the retry.
const unverifiedNote = "Catatan: jawaban ini belum diverifikasi dengan sumber."

// evidenceRetryNeeded reports whether a strict-grounding answer must be
// challenged: it carries more than a greeting, the turn consulted no tool, and
// it is not already an honest "not found".
func evidenceRetryNeeded(grounding, answer string, toolCalls int) bool {
	if grounding != config.GroundingStrict || toolCalls > 0 {
		return false
	}
	trimmed := strings.TrimSpace(answer)
	if len(trimmed) < 25 {
		return false
	}
	lowered := strings.ToLower(trimmed)
	for _, honest := range []string{
		"tidak menemukan", "tidak tahu", "tidak ada bukti", "tidak bisa memastikan",
		"saya adalah", "nama saya", "namaku", "i am", "my name is", "termixgo", "anes",
		"asisten", "bantuan apa", "ada yang bisa dibantu",
	} {
		if strings.Contains(lowered, honest) {
			return false
		}
	}
	return true
}

// setChannelStatus records the latest state of one running account.
func (a *App) setChannelStatus(key channel.AccountID, state string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if runtime, ok := a.channels[key]; ok {
		runtime.status = state
	}
	if key.Channel == config.ChannelTelegram && key.Account == config.FinalAgentName {
		a.botStatus = state
	}
	if state != "" && state != "connected" && state != "connecting" {
		fmt.Fprintf(os.Stderr, "channel %s/%s: %s\n", key.Channel, key.Account, state)
	}
}

// dropChannelRuntime removes an account that stopped on its own, so a retry
// can start it again.
func (a *App) dropChannelRuntime(key channel.AccountID) {
	a.mu.Lock()
	runtime := a.channels[key]
	delete(a.channels, key)
	if key.Channel == config.ChannelTelegram && key.Account == config.FinalAgentName && (runtime == nil || a.bot == runtime.bot) {
		a.bot = nil
		a.botCancel = nil
	}
	a.mu.Unlock()
}

// pairingStore opens the pairing file once per process.
func (a *App) pairingStore() *channel.PairingStore {
	a.mu.Lock()
	if a.pairing != nil {
		store := a.pairing
		a.mu.Unlock()
		return store
	}
	a.mu.Unlock()

	path, err := channel.DefaultPairingPath()
	if err != nil {
		return nil
	}
	store, err := channel.OpenPairingStore(path)
	if err != nil {
		return nil
	}
	a.mu.Lock()
	if a.pairing == nil {
		a.pairing = store
	}
	store = a.pairing
	a.mu.Unlock()
	return store
}

// startTelegramAccount starts one Telegram bot with its own bridge, policy and
// cursor. The default account also keeps the pre-registry fields in step, which
// is what the terminal status view and the scheduler read.
func (a *App) startTelegramAccount(name string) error {
	cfg := a.Config()
	entry, ok := cfg.TelegramAccount(name)
	if !ok {
		return fmt.Errorf("no telegram account %q", name)
	}
	if !entry.Enabled {
		return fmt.Errorf("telegram/%s is disabled; run 'termixgo channel on telegram %s' first", name, name)
	}
	token := strings.TrimSpace(a.store.Get(secrets.TelegramAccountTokenKey(name)))
	if token == "" {
		return fmt.Errorf("telegram/%s has no token; run 'termixgo channel login telegram %s <token>'", name, name)
	}

	bridge := &telegramBridge{app: a, name: name, account: entry}
	bot := telegram.New(token, bridge)
	bridge.bot = bot
	bot.SetAccount(name, entry.ChannelAccount, a.pairingStore())
	bot.Pair(entry.ChatID, entry.OwnerUserID)
	bot.SetPairingCode(entry.PairingCode)
	// The confirmed update id lives beside the rest of the state, scoped to
	// this bot id and account, so two bots never share a cursor.
	if id := telegram.BotIDFromToken(token); id != "" {
		if _, err := config.EnsureHome(); err == nil {
			file := "telegram-offset-" + id + ".txt"
			if name != config.FinalAgentName {
				file = "telegram-offset-" + id + "-" + name + ".txt"
			}
			if path, err := config.HomePath(file); err == nil {
				bot.SetCursorPath(path)
			}
		}
	}
	bot.OnPaired = func(chatID, ownerUserID int64) {
		if name == config.FinalAgentName {
			_ = a.SetTelegramChat(chatID, ownerUserID)
			return
		}
		_ = a.UpdateConfig(func(c *config.Config) {
			current, exists := c.TelegramAccount(name)
			if !exists {
				return
			}
			current.ChatID = chatID
			current.OwnerUserID = ownerUserID
			current.PairingCode = ""
			if chatID != 0 {
				current.Enabled = true
			}
			c.Channels.Telegram.Accounts[name] = current
		})
	}

	key := channel.AccountID{Channel: config.ChannelTelegram, Account: name}
	bot.Log = func(line string) {
		a.setChannelStatus(key, "error: "+line)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runtime := &channelRuntime{id: key, cancel: cancel, bot: bot, status: "connecting"}
	runtime.send = func(ctx context.Context, chatID, text string) error {
		id, err := strconv.ParseInt(strings.TrimSpace(chatID), 10, 64)
		if err != nil {
			return fmt.Errorf("telegram chat id %q is not a number", chatID)
		}
		return bot.SendTo(ctx, id, text)
	}
	runtime.sendFile = func(ctx context.Context, chatID, filename string, data []byte, caption string) error {
		id, err := strconv.ParseInt(strings.TrimSpace(chatID), 10, 64)
		if err != nil {
			return fmt.Errorf("telegram chat id %q is not a number", chatID)
		}
		return bot.SendFileTo(ctx, id, filename, data, caption)
	}
	a.mu.Lock()
	if a.channels == nil {
		a.channels = map[channel.AccountID]*channelRuntime{}
	}
	a.channels[key] = runtime
	if name == config.FinalAgentName {
		a.bot = bot
		a.botCancel = cancel
		a.botStatus = "connecting"
	}
	a.mu.Unlock()
	a.ensureOutboxWatcher()

	go func() {
		if _, err := bot.Verify(ctx); err != nil {
			a.setChannelStatus(key, "token rejected: "+err.Error())
			a.dropChannelRuntime(key)
			return
		}
		status := "connected"
		if bot.Paired() {
			status = "paired"
		} else if strings.TrimSpace(bot.PairingCode()) != "" {
			status = "waiting for pairing"
		}
		a.setChannelStatus(key, status)
		if err := bot.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			a.setChannelStatus(key, "stopped: "+err.Error())
		}
		// The loop exited on its own (token rejected, conflict, network
		// down): drop the runtime so a retry can start it again instead of
		// reporting a live account that no longer polls.
		a.dropChannelRuntime(key)
	}()
	return nil
}

// telegramBridge is the app side of one Telegram account: it owns the per-chat
// session and the approval route, and forwards the turn to the shared runner.
type telegramBridge struct {
	app     *App
	name    string
	account config.TelegramAccount
	bot     *telegram.Bot
}

// RunPrompt runs one turn for one chat. The session is the chat's own, never
// the operator's terminal conversation.
func (b *telegramBridge) RunPrompt(ctx context.Context, request channel.Request, prompt string, progress func(string)) (string, error) {
	session, err := b.session(request)
	if err != nil {
		return "", err
	}
	guest, maxSteps, budget := b.limits(request)
	answer, err := b.app.runChannelTurn(ctx, b.account.AgentName(b.name), session, b.interactor(), b.promptFor(request, prompt), nil, guest, maxSteps, budget, progress)
	if errors.Is(err, ErrBusy) {
		return "", channel.ErrBusy
	}
	if err != nil {
		return "", err
	}
	return b.app.RedactForChannel(answer), nil
}

// promptFor wraps a guest's message as data. The operator's own messages are
// instructions and stay as typed.
func (b *telegramBridge) promptFor(request channel.Request, prompt string) string {
	if request.Admin {
		return prompt
	}
	return channel.WrapUntrusted(prompt)
}

// RunPromptWithImage is RunPrompt with an image attached for a vision model.
func (b *telegramBridge) RunPromptWithImage(ctx context.Context, request channel.Request, prompt, mediaType, data string, progress func(string)) (string, error) {
	session, err := b.session(request)
	if err != nil {
		return "", err
	}
	images := []provider.Image{{MediaType: mediaType, Data: data}}
	guest, maxSteps, budget := b.limits(request)
	answer, err := b.app.runChannelTurn(ctx, b.account.AgentName(b.name), session, b.interactor(), b.promptFor(request, prompt), images, guest, maxSteps, budget, progress)
	if errors.Is(err, ErrBusy) {
		return "", channel.ErrBusy
	}
	if err != nil {
		return "", err
	}
	return b.app.RedactForChannel(answer), nil
}

// TranscribeAudio forwards voice/audio bytes to the app's configured STT engine.
func (b *telegramBridge) TranscribeAudio(ctx context.Context, data []byte, filename string) (string, error) {
	return b.app.TranscribeAudio(ctx, data, filename)
}

// RunVoicePrompt runs a voice-originated turn for one chat, falling back to the
// configured voice model when the primary model fails.
func (b *telegramBridge) RunVoicePrompt(ctx context.Context, request channel.Request, prompt string, progress func(string)) (string, error) {
	answer, err := b.RunPrompt(ctx, request, prompt, progress)
	if err == nil {
		return answer, nil
	}
	session, sessErr := b.session(request)
	if sessErr != nil {
		return "", err
	}
	return b.app.runChannelVoiceFallback(ctx, b.account.AgentName(b.name), session, b.promptFor(request, prompt), err, progress)
}

// ActiveProviders lists the providers configured and ready to use right now.
func (b *telegramBridge) ActiveProviders() []provider.Provider {
	return b.app.ActiveProviders()
}

// ModelsForProvider lists catalogue and custom models for a provider.
func (b *telegramBridge) ModelsForProvider(providerID string) []provider.Model {
	return b.app.ModelsForProvider(providerID)
}

// CostInfo renders the token and dollar spend for the chat's session.
func (b *telegramBridge) CostInfo(request channel.Request, lang string) string {
	session, err := b.session(request)
	if err != nil {
		return b.app.sessionCostInfo(nil, lang)
	}
	return b.app.sessionCostInfo(session, lang)
}

// PlanInfo renders the active plan/todos for the chat's session.
func (b *telegramBridge) PlanInfo(request channel.Request, lang string) string {
	session, err := b.session(request)
	if err != nil {
		return b.app.sessionPlanInfo(nil, lang)
	}
	return b.app.sessionPlanInfo(session, lang)
}

// CompactSession condenses earlier turns of the chat's session into a brief.
func (b *telegramBridge) CompactSession(ctx context.Context, request channel.Request, lang string) string {
	session, err := b.session(request)
	if err != nil {
		return b.app.compactChannelSession(ctx, nil, lang)
	}
	return b.app.compactChannelSession(ctx, session, lang)
}

// limits bounds a guest turn: an operator's run uses the configured ceilings,
// a guest run the account's own, which are tighter by default.
func (b *telegramBridge) limits(request channel.Request) (bool, int, float64) {
	if request.Admin {
		return false, 0, 0
	}
	limits := b.account.EffectiveGuestLimits()
	return true, limits.MaxSteps, limits.CostBudgetUSD
}

// NewSession resets one chat's conversation: the file is rewritten empty, so
// the next turn loads a fresh session with the same id.
func (b *telegramBridge) NewSession(request channel.Request) {
	session, err := b.freshSession(request)
	if err != nil {
		return
	}
	session.SetTodos(nil)
	_ = session.Save()
}

func (b *telegramBridge) Stop() { b.app.Stop() }

func (b *telegramBridge) Model() string { return b.app.ModelLabel() }

func (b *telegramBridge) SetModel(query string) (string, error) { return b.app.SetModel(query) }

func (b *telegramBridge) Models() string { return b.app.AvailableModelsText("en") }

func (b *telegramBridge) ModelsFor(lang string) string { return b.app.AvailableModelsText(lang) }

func (b *telegramBridge) GitDiff(ctx context.Context) (string, error) {
	diff, err := b.app.GitDiff(ctx)
	if err != nil {
		return "", err
	}
	return b.app.RedactForChannel(diff), nil
}

func (b *telegramBridge) Status() string { return b.app.Status() }

func (b *telegramBridge) Workspace() string {
	return b.app.AgentWorkspace(b.account.AgentName(b.name))
}

func (b *telegramBridge) interactor() Interactor {
	if b.bot == nil {
		return nil
	}
	return &telegramInteractor{bot: b.bot}
}

// session loads the chat's session, or starts one with the chat's stable id.
func (b *telegramBridge) session(request channel.Request) (*agent.Session, error) {
	agentName := b.account.AgentName(b.name)
	dir, err := agenthome.SessionsDirFor(agentName)
	if err != nil {
		return nil, err
	}
	id := channelSessionID(config.ChannelTelegram, b.name, request.ChatID)
	if loaded, err := agent.LoadSessionIn(dir, id); err == nil {
		loaded.SetAgent(agentName)
		return loaded, nil
	}
	session, err := agent.NewSessionWithID(agentName, id, b.app.AgentWorkspace(agentName), b.app.CurrentModel().ID)
	if err != nil {
		return nil, err
	}
	session.SetStoreDir(dir)
	return session, nil
}

func (b *telegramBridge) freshSession(request channel.Request) (*agent.Session, error) {
	agentName := b.account.AgentName(b.name)
	dir, err := agenthome.SessionsDirFor(agentName)
	if err != nil {
		return nil, err
	}
	id := channelSessionID(config.ChannelTelegram, b.name, request.ChatID)
	session, err := agent.NewSessionWithID(agentName, id, b.app.AgentWorkspace(agentName), b.app.CurrentModel().ID)
	if err != nil {
		return nil, err
	}
	session.SetStoreDir(dir)
	return session, nil
}

// channelSessionID is the stable session id of one chat: the transport, the
// account and the chat, folded onto the characters a session file name accepts.
func channelSessionID(kind, account, chatID string) string {
	return "chan-" + sanitizeSessionPart(kind) + "-" + sanitizeSessionPart(account) + "-" + sanitizeSessionPart(chatID)
}

func sanitizeSessionPart(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}
	out := builder.String()
	if len(out) > 64 {
		out = out[:64]
	}
	if strings.Trim(out, "-") == "" {
		out = "chat"
	}
	return out
}

// runChannelVoiceFallback retries a failed voice-originated turn with the
// configured voice fallback model on the chat's own session.
func (a *App) runChannelVoiceFallback(ctx context.Context, agentName string, session *agent.Session, prompt string, primaryErr error, progress func(string)) (string, error) {
	a.mu.Lock()
	voiceModelID := strings.TrimSpace(a.cfg.VoiceModel)
	maxSteps := a.cfg.MaxSteps
	a.mu.Unlock()

	if voiceModelID == "" {
		return "", primaryErr
	}
	if progress != nil {
		progress(fmt.Sprintf("Falling back to voice model (%s)...", voiceModelID))
	}
	roleClient, roleModel, clientErr := a.subagentClient(voiceModelID)
	if clientErr != nil {
		return "", fmt.Errorf("active turn failed (%v), and voice fallback model error: %w", primaryErr, clientErr)
	}
	report, spend, subErr := agent.RunSubagent(ctx, a.envFor(agentName), roleClient, roleModel, string(agent.SubagentGeneral), prompt, maxSteps)
	if subErr != nil {
		return "", fmt.Errorf("active turn failed (%v), and voice fallback turn error: %w", primaryErr, subErr)
	}
	if session != nil {
		session.AddUser(prompt)
		session.AddAssistant(report, "", nil, nil)
		session.AddUsage(spend.Usage)
		_ = session.Save()
	}
	a.AddUsage(spend.Usage)
	return a.RedactForChannel(report), nil
}

// sessionCostInfo formats token and dollar spend for one channel session.
func (a *App) sessionCostInfo(session *agent.Session, lang string) string {
	if session == nil {
		if lang == "id" {
			return "Belum ada sesi aktif."
		}
		return "No active session."
	}
	usage := session.Usage()
	cost := session.Cost()
	turns := session.Turns()
	model := a.CurrentModel()
	modelName := a.ModelLabel()
	if modelName == "" {
		modelName = model.ID
	}
	if lang == "id" {
		lines := []string{
			fmt.Sprintf("Sesi: %s (%d giliran)", session.ID(), turns),
			fmt.Sprintf("Model aktif: %s", orNone(modelName)),
			fmt.Sprintf("Token masuk: %d", usage.PromptTokens),
			fmt.Sprintf("Token keluar: %d", usage.CompletionTokens),
			fmt.Sprintf("Total token: %d", usage.TotalTokens),
		}
		if cost > 0 {
			lines = append(lines, fmt.Sprintf("Estimasi biaya: $%.4f USD", cost))
		} else {
			lines = append(lines, "Estimasi biaya: $0.0000 USD (atau paket langganan)")
		}
		return strings.Join(lines, "\n")
	}
	lines := []string{
		fmt.Sprintf("Session: %s (%d turn(s))", session.ID(), turns),
		fmt.Sprintf("Active model: %s", orNone(modelName)),
		fmt.Sprintf("Prompt tokens: %d", usage.PromptTokens),
		fmt.Sprintf("Completion tokens: %d", usage.CompletionTokens),
		fmt.Sprintf("Total tokens: %d", usage.TotalTokens),
	}
	if cost > 0 {
		lines = append(lines, fmt.Sprintf("Estimated cost: $%.4f USD", cost))
	} else {
		lines = append(lines, "Estimated cost: $0.0000 USD (or subscription plan)")
	}
	return strings.Join(lines, "\n")
}

// sessionPlanInfo formats the active plan/todos for one channel session.
func (a *App) sessionPlanInfo(session *agent.Session, lang string) string {
	var todos []agent.Todo
	if session != nil {
		todos = session.Todos()
	}
	if len(todos) == 0 && a.Todos() != nil {
		todos = a.Todos().Items()
	}
	if len(todos) == 0 {
		if lang == "id" {
			return "Belum ada rencana (plan) aktif pada sesi ini."
		}
		return "No active plan in this session."
	}
	done := 0
	lines := make([]string, 0, len(todos)+1)
	for index, item := range todos {
		mark := "[ ]"
		switch item.Status {
		case "completed":
			mark = "[x]"
			done++
		case "in_progress":
			mark = "[>]"
		}
		lines = append(lines, fmt.Sprintf("%d. %s %s", index+1, mark, item.Title))
	}
	header := fmt.Sprintf("Plan (%d/%d complete):", done, len(todos))
	if lang == "id" {
		header = fmt.Sprintf("Rencana (%d/%d selesai):", done, len(todos))
	}
	return header + "\n" + strings.Join(lines, "\n")
}

// compactChannelSession manually condenses a channel session's history.
func (a *App) compactChannelSession(ctx context.Context, session *agent.Session, lang string) string {
	ok, err := a.ForceCompactSession(ctx, session)
	if err != nil {
		if lang == "id" {
			return "Gagal meringkas riwayat percakapan: " + err.Error()
		}
		return "Could not compact session history: " + err.Error()
	}
	if !ok {
		if lang == "id" {
			return "Riwayat percakapan belum perlu diringkas."
		}
		return "Session history did not need compaction."
	}
	if lang == "id" {
		return "Riwayat percakapan berhasil diringkas untuk menghemat ruang konteks."
	}
	return "Condensed earlier turns into a brief to free up context space."
}

// maxChannelAttachmentBytes caps outgoing channel attachments at 50 MB.
const maxChannelAttachmentBytes = 50 * 1024 * 1024

// SendChannel delivers a message and/or file attachment from the active workspace
// to Telegram or WhatsApp.
func (a *App) SendChannel(ctx context.Context, req agent.ChannelSendRequest) (string, error) {
	return a.sendChannelFrom(ctx, a.Workspace(), req)
}

func (a *App) sendChannelFrom(ctx context.Context, workspace string, req agent.ChannelSendRequest) (string, error) {
	kind := normalizeChannelKind(req.Channel)
	if kind == "" {
		return "", fmt.Errorf("unknown channel %q; use \"telegram\" or \"whatsapp\"", req.Channel)
	}
	message := a.RedactForChannel(strings.TrimSpace(req.Message))
	filePath := strings.TrimSpace(req.FilePath)
	if filePath == "" && message == "" {
		return "", errors.New("provide at least one of file_path or message")
	}

	var fileData []byte
	var filename string
	if filePath != "" {
		resolvedPath := filePath
		if !filepath.IsAbs(resolvedPath) {
			base := strings.TrimSpace(workspace)
			if base == "" {
				base = a.Workspace()
			}
			resolvedPath = filepath.Join(base, resolvedPath)
		}
		info, err := os.Stat(resolvedPath)
		if err != nil {
			return "", fmt.Errorf("attachment file %q: %w", req.FilePath, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("attachment path %q is a directory, not a file", req.FilePath)
		}
		if info.Size() > maxChannelAttachmentBytes {
			return "", fmt.Errorf("attachment %q is %d bytes (limit %d bytes)", req.FilePath, info.Size(), maxChannelAttachmentBytes)
		}
		data, err := os.ReadFile(resolvedPath)
		if err != nil {
			return "", fmt.Errorf("read attachment %q: %w", req.FilePath, err)
		}
		fileData = data
		filename = filepath.Base(resolvedPath)
	}

	switch kind {
	case config.ChannelTelegram:
		return a.sendTelegramChannel(ctx, req.Account, req.Recipient, filename, fileData, message)
	case config.ChannelWhatsApp:
		return a.sendWhatsAppChannel(ctx, req.Account, req.Recipient, filename, fileData, message)
	default:
		return "", fmt.Errorf("unsupported channel %q", kind)
	}
}

func normalizeChannelKind(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "telegram", "tg":
		return config.ChannelTelegram
	case "whatsapp", "wa":
		return config.ChannelWhatsApp
	default:
		return ""
	}
}

func (a *App) sendTelegramChannel(ctx context.Context, rawAccount, rawRecipient, filename string, fileData []byte, message string) (string, error) {
	cfg := a.Config()
	account := strings.TrimSpace(rawAccount)
	if account == "" {
		account = config.FinalAgentName
	}
	entry, ok := cfg.TelegramAccount(account)
	if !ok {
		for name, acc := range cfg.Channels.Telegram.Accounts {
			account = name
			entry = acc
			ok = true
			if acc.Enabled {
				break
			}
		}
	}

	key := channel.AccountID{Channel: config.ChannelTelegram, Account: account}
	a.mu.Lock()
	runtime := a.channels[key]
	a.mu.Unlock()

	var chatID int64
	if strings.TrimSpace(rawRecipient) != "" {
		parsed, err := strconv.ParseInt(strings.TrimSpace(rawRecipient), 10, 64)
		if err != nil {
			return "", fmt.Errorf("telegram recipient %q must be a numeric chat ID", rawRecipient)
		}
		chatID = parsed
	} else {
		if ok && entry.ChatID != 0 {
			chatID = entry.ChatID
		} else if cfg.Telegram.ChatID != 0 {
			chatID = cfg.Telegram.ChatID
		} else if ok && entry.OwnerUserID != 0 {
			chatID = entry.OwnerUserID
		} else if cfg.Telegram.OwnerUserID != 0 {
			chatID = cfg.Telegram.OwnerUserID
		} else if runtime != nil && runtime.bot != nil {
			chatID, _ = runtime.bot.Pairing()
		}
	}
	if chatID == 0 {
		return "", fmt.Errorf("no Telegram chat ID is configured for telegram/%s; pass recipient or pair the Telegram bot first", account)
	}

	chatIDStr := strconv.FormatInt(chatID, 10)
	if len(fileData) > 0 {
		if runtime != nil && runtime.sendFile != nil {
			if err := runtime.sendFile(ctx, chatIDStr, filename, fileData, message); err != nil {
				return "", err
			}
			return fmt.Sprintf("Sent attachment %q (%d bytes) to telegram/%s (chat %d).", filename, len(fileData), account, chatID), nil
		}
		token := a.telegramTokenFor(account)
		if token == "" {
			return "", fmt.Errorf("no token stored for telegram/%s", account)
		}
		client := telegram.NewClient(token)
		if _, err := client.SendDocument(ctx, chatID, filename, fileData, message); err != nil {
			return "", err
		}
		return fmt.Sprintf("Sent attachment %q (%d bytes) to telegram/%s (chat %d).", filename, len(fileData), account, chatID), nil
	}

	if runtime != nil && runtime.send != nil {
		if err := runtime.send(ctx, chatIDStr, message); err != nil {
			return "", err
		}
		return fmt.Sprintf("Sent message to telegram/%s (chat %d).", account, chatID), nil
	}
	token := a.telegramTokenFor(account)
	if token == "" {
		return "", fmt.Errorf("no token stored for telegram/%s", account)
	}
	client := telegram.NewClient(token)
	limit := entry.EffectiveTextChunkLimit()
	for _, chunk := range channel.Chunk(message, limit) {
		if _, err := client.SendMarkdown(ctx, chatID, chunk, nil); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("Sent message to telegram/%s (chat %d).", account, chatID), nil
}

func (a *App) telegramTokenFor(account string) string {
	if a.store == nil {
		return ""
	}
	if tok := strings.TrimSpace(a.store.Get(secrets.TelegramAccountTokenKey(account))); tok != "" {
		return tok
	}
	return strings.TrimSpace(a.store.Get(secrets.TelegramTokenKey()))
}

func (a *App) sendWhatsAppChannel(ctx context.Context, rawAccount, rawRecipient, filename string, fileData []byte, message string) (string, error) {
	cfg := a.Config()
	account := strings.TrimSpace(rawAccount)
	if account == "" {
		account = config.FinalAgentName
	}
	entry, ok := cfg.WhatsAppAccount(account)
	if !ok {
		for name, acc := range cfg.Channels.WhatsApp.Accounts {
			account = name
			entry = acc
			ok = true
			if acc.Enabled {
				break
			}
		}
	}

	recipient := whatsapp.NormalizeJID(rawRecipient)
	if recipient == "" && ok {
		recipient = defaultWhatsAppRecipient(entry)
	}

	key := channel.AccountID{Channel: config.ChannelWhatsApp, Account: account}
	a.mu.Lock()
	runtime := a.channels[key]
	a.mu.Unlock()

	if recipient == "" && runtime != nil && runtime.waBot != nil {
		if self := strings.TrimSpace(runtime.waBot.SelfID()); self != "" {
			recipient = whatsapp.NormalizeJID(whatsapp.PhoneFromJID(self))
		}
	}
	if recipient == "" {
		return "", fmt.Errorf("no WhatsApp recipient configured for whatsapp/%s; pass recipient phone number (for example \"628123456789\") or configure an admin on the account", account)
	}

	// 1. If the WhatsApp bot is running and connected in this process, send directly.
	if runtime != nil && (runtime.waBot == nil || runtime.waBot.Connected()) {
		if len(fileData) > 0 && runtime.sendFile != nil {
			if err := runtime.sendFile(ctx, recipient, filename, fileData, message); err != nil {
				return "", err
			}
			return fmt.Sprintf("Sent attachment %q (%d bytes) to whatsapp/%s (%s).", filename, len(fileData), account, recipient), nil
		}
		if len(fileData) == 0 && runtime.send != nil {
			if err := runtime.send(ctx, recipient, message); err != nil {
				return "", err
			}
			return fmt.Sprintf("Sent message to whatsapp/%s (%s).", account, recipient), nil
		}
	}

	// 2. If another process (such as `termixgo serve`) owns the live WhatsApp
	// connection, hand the delivery to it through the local outbox directory.
	if handled, err := a.deliverViaOutbox(ctx, config.ChannelWhatsApp, account, recipient, filename, fileData, message); handled {
		if err != nil {
			return "", err
		}
		if len(fileData) > 0 {
			return fmt.Sprintf("Sent attachment %q (%d bytes) to whatsapp/%s (%s).", filename, len(fileData), account, recipient), nil
		}
		return fmt.Sprintf("Sent message to whatsapp/%s (%s).", account, recipient), nil
	}

	// 3. Otherwise, start the WhatsApp account in this process and wait briefly
	// for the connection to come up.
	if runtime == nil {
		if err := a.StartChannel(config.ChannelWhatsApp, account); err != nil {
			return "", err
		}
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		runtime = a.channels[key]
		a.mu.Unlock()
		if runtime != nil && (runtime.waBot == nil || runtime.waBot.Connected()) {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if runtime == nil {
		return "", fmt.Errorf("whatsapp/%s is not running", account)
	}
	if len(fileData) > 0 {
		if runtime.sendFile == nil {
			return "", fmt.Errorf("whatsapp/%s does not support file attachments", account)
		}
		if err := runtime.sendFile(ctx, recipient, filename, fileData, message); err != nil {
			return "", err
		}
		return fmt.Sprintf("Sent attachment %q (%d bytes) to whatsapp/%s (%s).", filename, len(fileData), account, recipient), nil
	}
	if runtime.send == nil {
		return "", fmt.Errorf("whatsapp/%s is not ready to send", account)
	}
	if err := runtime.send(ctx, recipient, message); err != nil {
		return "", err
	}
	return fmt.Sprintf("Sent message to whatsapp/%s (%s).", account, recipient), nil
}

func defaultWhatsAppRecipient(entry config.ChannelAccount) string {
	candidates := append([]string(nil), entry.Admins...)
	candidates = append(candidates, entry.AllowFrom...)
	// Prefer a standard phone number (such as 628...) or @s.whatsapp.net JID
	// over an opaque 15-digit LID when selecting the default recipient.
	for _, raw := range candidates {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasSuffix(trimmed, "@lid") {
			continue
		}
		if strings.HasSuffix(trimmed, "@s.whatsapp.net") {
			return trimmed
		}
		if len(trimmed) <= 14 {
			return whatsapp.NormalizeJID(trimmed)
		}
	}
	if len(candidates) > 0 {
		return whatsapp.NormalizeJID(candidates[0])
	}
	return ""
}

type channelOutboxRequest struct {
	ID        string `json:"id"`
	Channel   string `json:"channel"`
	Account   string `json:"account"`
	Recipient string `json:"recipient"`
	Filename  string `json:"filename,omitempty"`
	Data      []byte `json:"data,omitempty"`
	Message   string `json:"message,omitempty"`
}

type channelOutboxResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func outboxDir() (string, error) {
	dir, err := config.HomePath("channel-outbox")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func (a *App) ensureOutboxWatcher() {
	a.mu.Lock()
	if a.outboxCancel != nil {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.outboxCancel = cancel
	a.mu.Unlock()

	go func() {
		ticker := time.NewTicker(350 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.processOutboxFiles(ctx)
			}
		}
	}()
}

func (a *App) processOutboxFiles(ctx context.Context) {
	dir, err := outboxDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "req-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		reqPath := filepath.Join(dir, name)
		raw, err := os.ReadFile(reqPath)
		if err != nil {
			continue
		}
		var req channelOutboxRequest
		if err := json.Unmarshal(raw, &req); err != nil || req.ID == "" {
			_ = os.Remove(reqPath)
			continue
		}
		key := channel.AccountID{Channel: req.Channel, Account: req.Account}
		a.mu.Lock()
		runtime := a.channels[key]
		a.mu.Unlock()
		if runtime == nil || (runtime.waBot != nil && !runtime.waBot.Connected()) {
			continue
		}
		_ = os.Remove(reqPath)
		var sendErr error
		if len(req.Data) > 0 {
			if runtime.sendFile != nil {
				sendErr = runtime.sendFile(ctx, req.Recipient, req.Filename, req.Data, req.Message)
			} else {
				sendErr = fmt.Errorf("%s/%s does not support file delivery", req.Channel, req.Account)
			}
		} else if runtime.send != nil {
			sendErr = runtime.send(ctx, req.Recipient, req.Message)
		} else {
			sendErr = fmt.Errorf("%s/%s is not ready", req.Channel, req.Account)
		}
		res := channelOutboxResponse{OK: sendErr == nil}
		if sendErr != nil {
			res.Error = sendErr.Error()
		}
		if encoded, err := json.Marshal(res); err == nil {
			resPath := filepath.Join(dir, "res-"+req.ID+".json")
			_ = os.WriteFile(resPath, encoded, 0o600)
		}
	}
}

func (a *App) deliverViaOutbox(ctx context.Context, ch, account, recipient, filename string, fileData []byte, message string) (bool, error) {
	dir, err := outboxDir()
	if err != nil {
		return false, nil
	}
	id := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	req := channelOutboxRequest{
		ID:        id,
		Channel:   ch,
		Account:   account,
		Recipient: recipient,
		Filename:  filename,
		Data:      fileData,
		Message:   message,
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		return false, nil
	}
	reqPath := filepath.Join(dir, "req-"+id+".json")
	resPath := filepath.Join(dir, "res-"+id+".json")
	if err := os.WriteFile(reqPath, encoded, 0o600); err != nil {
		return false, nil
	}
	defer func() {
		_ = os.Remove(reqPath)
		_ = os.Remove(resPath)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(resPath); err == nil {
			var res channelOutboxResponse
			if err := json.Unmarshal(raw, &res); err == nil {
				if !res.OK {
					return true, errors.New(res.Error)
				}
				return true, nil
			}
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return false, nil
}
