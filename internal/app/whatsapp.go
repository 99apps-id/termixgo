package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/agenthome"
	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/whatsapp"
)

// approvalTimeout bounds how long an in-chat approval waits for the operator
// to answer before it fails closed, the same window the Telegram buttons get.
var approvalTimeout = 3 * time.Minute

// A separate CLI process can grant or revoke an admin while the bot runs.
// Config.Save replaces the file atomically, so an unchanged file can reuse
// its parsed admin list. An unreadable configuration grants no listed admins.
func whatsAppAdmins(account string) func() []string {
	var mu sync.Mutex
	var previous os.FileInfo
	var admins []string
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		path, err := config.Path()
		if err != nil {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil
		}
		if previous != nil && previous.ModTime() == info.ModTime() && previous.Size() == info.Size() && os.SameFile(previous, info) {
			return admins
		}
		cfg, err := config.Load()
		if err != nil {
			return nil
		}
		entry, ok := cfg.WhatsAppAccount(account)
		admins = nil
		if ok && entry.Enabled {
			admins = entry.Admins
		}
		previous = info
		return admins
	}
}

// startWhatsAppAccount starts one linked-device account.
//
// An account that was never linked is not started: connecting without a device
// identity would open a QR pairing window inside a long-running assistant,
// which nobody is watching. The operator links it with `channel login`.
func (a *App) startWhatsAppAccount(name string) error {
	cfg := a.Config()
	entry, ok := cfg.WhatsAppAccount(name)
	if !ok {
		return fmt.Errorf("no whatsapp account %q", name)
	}
	if !entry.Enabled {
		return fmt.Errorf("whatsapp/%s is disabled; run 'termixgo channel on whatsapp %s' first", name, name)
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
	if device.ID == nil {
		_ = closeStore()
		return fmt.Errorf("whatsapp/%s is not linked yet; run 'termixgo channel login whatsapp %s'", name, name)
	}

	client := whatsapp.Client(device)
	bridge := &whatsappBridge{app: a, name: name, account: entry}
	bot := whatsapp.New(client, bridge)
	bridge.send = bot.SendTo
	bot.SetAccount(name, entry, a.pairingStore())
	bot.SetAdminSource(whatsAppAdmins(name))
	bot.SetRedactor(a.RedactForChannel)

	key := channel.AccountID{Channel: config.ChannelWhatsApp, Account: name}
	bot.SetLogger(func(line string) {
		a.setChannelStatus(key, line)
	})

	ctx, cancel := context.WithCancel(context.Background())
	runtime := &channelRuntime{
		id:       key,
		cancel:   cancel,
		waBot:    bot,
		status:   "connecting",
		send:     bot.SendTo,
		sendFile: bot.SendFileTo,
	}
	a.mu.Lock()
	if a.channels == nil {
		a.channels = map[channel.AccountID]*channelRuntime{}
	}
	a.channels[key] = runtime
	a.mu.Unlock()
	a.ensureOutboxWatcher()

	go func() {
		defer closeStore()
		a.setChannelStatus(key, "connecting")
		lockName := fmt.Sprintf("poller-%s-%s.lock", key.Channel, key.Account)
		lock, lockPID, lockErr := config.TryAcquireInstanceLock(lockName)
		if errors.Is(lockErr, config.ErrAlreadyLocked) {
			a.setChannelStatus(key, fmt.Sprintf("active in background (PID %d)", lockPID))
			return
		}
		if lockErr != nil {
			a.setChannelStatus(key, "lock error: "+lockErr.Error())
			return
		}
		defer lock.Release()

		notifier, alertErr := a.ensureGatewayNotifier()
		if alertErr != nil {
			a.emit(agent.Event{Kind: agent.EventNotice, Text: "Gateway alert queue unavailable: " + alertErr.Error()})
		} else {
			notifier.record(a, key, "starting")
		}
		bot.SetLifecycleObserver(func(state string) {
			if state == "online" {
				a.setChannelStatus(key, "connected")
			} else {
				a.setChannelStatus(key, "disconnected")
			}
			if notifier != nil {
				notifier.record(a, key, state)
			}
		})
		if err := bot.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			a.setChannelStatus(key, "stopped: "+err.Error())
		}
		a.dropChannelRuntime(key)
	}()
	return nil
}

type pendingWhatsAppApproval struct {
	answer chan agent.Decision
	chat   string
	sender string
}

// whatsappBridge is the app side of one WhatsApp account. Sessions are per
// chat, under the owning agent's home, and the turn runs through the same
// runner as every other surface.
type whatsappBridge struct {
	app     *App
	name    string
	account config.ChannelAccount
	send    func(ctx context.Context, chatID, text string) error

	// pendingApprovals holds at most one waiting approval per chat, keyed by
	// CanonicalID(chatID), so concurrent WhatsApp chats do not clobber each other.
	mu               sync.Mutex
	pendingApprovals map[string]*pendingWhatsAppApproval
}

// interactorFor returns the approval route for one chat. An admin answers in
// the chat the turn came from; a guest gets none, because the guest tool set
// already strips the mutating tools and an answer must come from the operator.
func (b *whatsappBridge) interactorFor(request channel.Request) Interactor {
	if !request.Admin {
		return nil
	}
	return &whatsAppApprover{bridge: b, request: request}
}

// whatsAppApprover routes one turn's approvals to the operator's chat.
type whatsAppApprover struct {
	bridge  *whatsappBridge
	request channel.Request
}

func (w *whatsAppApprover) Approve(request agent.ApprovalRequest) agent.Decision {
	return w.bridge.askApproval(context.Background(), w.request, request)
}

func (w *whatsAppApprover) ApproveContext(ctx context.Context, request agent.ApprovalRequest) agent.Decision {
	return w.bridge.askApproval(ctx, w.request, request)
}

func (w *whatsAppApprover) Ask(string, []string) (string, error) {
	return "", errors.New("interactive questions are not supported over WhatsApp yet")
}

// askApproval posts the request in the chat and waits for the operator's
// reply or turn cancellation. No answer inside the window is a denial.
func (b *whatsappBridge) askApproval(ctx context.Context, request channel.Request, approval agent.ApprovalRequest) agent.Decision {
	if b.send == nil {
		return agent.DecisionDeny
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lang := whatsapp.SenderLanguage(request)
	if err := b.send(ctx, request.ChatID, approvalPromptText(lang, approval)); err != nil {
		return agent.DecisionDeny
	}
	key := channel.CanonicalID(request.ChatID)
	entry := &pendingWhatsAppApproval{
		answer: make(chan agent.Decision, 1),
		chat:   request.ChatID,
		sender: request.SenderID,
	}
	b.mu.Lock()
	if b.pendingApprovals == nil {
		b.pendingApprovals = make(map[string]*pendingWhatsAppApproval)
	}
	b.pendingApprovals[key] = entry
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.pendingApprovals[key] == entry {
			delete(b.pendingApprovals, key)
		}
		b.mu.Unlock()
	}()
	timer := time.NewTimer(approvalTimeout)
	defer timer.Stop()
	select {
	case decision := <-entry.answer:
		return decision
	case <-timer.C:
		_ = b.send(context.Background(), request.ChatID, approvalTimeoutText(lang))
		return agent.DecisionDeny
	case <-ctx.Done():
		return agent.DecisionDeny
	}
}

// ResolveApproval answers the pending in-chat approval from the operator's
// reply, reporting whether the message was consumed. Only an admin or the
// turn's sender in the waiting chat may answer; anyone else's text runs as its own turn.
func (b *whatsappBridge) ResolveApproval(request channel.Request, text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.HasPrefix(trimmed, "/") {
		return false
	}
	key := channel.CanonicalID(request.ChatID)
	b.mu.Lock()
	entry := b.pendingApprovals[key]
	if entry == nil {
		b.mu.Unlock()
		return false
	}
	if !request.Admin {
		b.mu.Unlock()
		return false
	}
	delete(b.pendingApprovals, key)
	b.mu.Unlock()

	decision := mapApprovalAnswer(trimmed)
	_ = b.send(context.Background(), request.ChatID, approvalResultText(whatsapp.SenderLanguage(request), decision))
	select {
	case entry.answer <- decision:
	default:
	}
	return true
}

// HasPendingApproval reports whether an approval is actively waiting in request's chat.
func (b *whatsappBridge) HasPendingApproval(request channel.Request) bool {
	key := channel.CanonicalID(request.ChatID)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pendingApprovals[key] != nil
}

// mapApprovalAnswer turns the operator's reply into a decision. Anything
// unrecognised fails closed: a waiting turn must never read silence or a
// stray word as consent.
func mapApprovalAnswer(text string) agent.Decision {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "y", "yes", "ya", "iya", "allow", "ok", "1":
		return agent.DecisionAllowOnce
	case "s", "session", "sesi", "2":
		return agent.DecisionAllowSession
	case "a", "always", "selalu", "3":
		return agent.DecisionAllowAlways
	default:
		return agent.DecisionDeny
	}
}

// approvalPromptText renders the in-chat request in English, matching the
// operator's preference for approval cards across channels.
func approvalPromptText(_ string, request agent.ApprovalRequest) string {
	detail := agent.Shorten(request.Detail, 160)
	return fmt.Sprintf("Approval needed: %s (%s)\n%s\nReply y (once), s (session), a (always), n (deny). %d minutes.",
		request.Tool, request.Risk, detail, int(approvalTimeout.Minutes()))
}

func approvalTimeoutText(_ string) string {
	return "Approval timed out, denied."
}

func approvalResultText(_ string, decision agent.Decision) string {
	switch decision {
	case agent.DecisionAllowOnce:
		return "Allowed once."
	case agent.DecisionAllowSession:
		return "Allowed for this session."
	case agent.DecisionAllowAlways:
		return "Always allowed."
	default:
		return "Denied."
	}
}

// RunPrompt runs one turn for one chat.
func (b *whatsappBridge) RunPrompt(ctx context.Context, request channel.Request, prompt string, progress func(string)) (string, error) {
	return b.runPrompt(ctx, request, prompt, nil, false, progress)
}

func (b *whatsappBridge) runPrompt(ctx context.Context, request channel.Request, prompt string, images []provider.Image, voice bool, progress func(string)) (string, error) {
	session, err := b.session(request)
	if err != nil {
		return "", err
	}
	guest, maxSteps, budget := b.limits(request)
	answer, err := b.app.runChannelTurn(ctx, b.account.AgentName(b.name), session, b.interactorFor(request), b.promptFor(request, prompt), images, guest, maxSteps, budget, request.SenderName, "WhatsApp", request.ChatID, b.name, voice, progress, nil)
	if errors.Is(err, ErrBusy) {
		return "", channel.ErrBusy
	}
	if err != nil {
		return "", err
	}
	return b.app.RedactForChannel(answer), nil
}

// promptFor wraps a guest's message as data; the operator's messages are
// instructions and stay as typed.
func (b *whatsappBridge) promptFor(request channel.Request, prompt string) string {
	if request.Admin {
		return prompt
	}
	return channel.WrapUntrusted(prompt)
}

// RunPromptWithImage is RunPrompt with an image attached for a vision model.
func (b *whatsappBridge) RunPromptWithImage(ctx context.Context, request channel.Request, prompt, mediaType, data string, progress func(string)) (string, error) {
	return b.runPrompt(ctx, request, prompt, []provider.Image{{MediaType: mediaType, Data: data}}, false, progress)
}

// Steer forwards a steering message into an active turn in this chat.
func (b *whatsappBridge) Steer(request channel.Request, prompt string) bool {
	id := channelSessionKey(config.ChannelWhatsApp, b.name, b.account, request)
	// A guest's steer is data like any guest prompt, never a raw instruction.
	return b.app.SteerSession(id, b.promptFor(request, prompt))
}

// IsRunning reports whether a turn is active for this chat.
func (b *whatsappBridge) IsRunning(request channel.Request) bool {
	id := channelSessionKey(config.ChannelWhatsApp, b.name, b.account, request)
	return b.app.IsSessionSteerable(id)
}

// StopChat cancels the turn in flight for this chat.
func (b *whatsappBridge) StopChat(request channel.Request) bool {
	id := channelSessionKey(config.ChannelWhatsApp, b.name, b.account, request)
	return b.app.StopSession(id)
}

// TranscribeAudio forwards voice/audio bytes to the app's configured STT engine.
func (b *whatsappBridge) TranscribeAudio(ctx context.Context, data []byte, filename string) (string, error) {
	return b.app.TranscribeAudio(ctx, data, filename)
}

// RunVoicePrompt runs a voice-originated turn for one chat, falling back to the
// configured voice model when the primary model fails.
func (b *whatsappBridge) RunVoicePrompt(ctx context.Context, request channel.Request, prompt string, progress func(string)) (string, error) {
	return b.runPrompt(ctx, request, prompt, nil, true, progress)
}

// ActiveProviders lists the providers configured and ready to use right now.
func (b *whatsappBridge) ActiveProviders() []provider.Provider {
	return b.app.ActiveProviders()
}

// ModelsForProvider lists catalogue and custom models for a provider.
func (b *whatsappBridge) ModelsForProvider(providerID string) []provider.Model {
	return b.app.ModelsForProvider(providerID)
}

// CostInfo renders the token and dollar spend for the chat's session.
func (b *whatsappBridge) CostInfo(request channel.Request, lang string) string {
	session, err := b.session(request)
	if err != nil {
		return b.app.sessionCostInfo(nil, lang)
	}
	return b.app.sessionCostInfo(session, lang)
}

// PlanInfo renders the active plan/todos for the chat's session.
func (b *whatsappBridge) PlanInfo(request channel.Request, lang string) string {
	session, err := b.session(request)
	if err != nil {
		return b.app.sessionPlanInfo(nil, lang)
	}
	return b.app.sessionPlanInfo(session, lang)
}

// CompactSession condenses earlier turns of the chat's session into a brief.
func (b *whatsappBridge) CompactSession(ctx context.Context, request channel.Request, lang string) string {
	session, err := b.session(request)
	if err != nil {
		return b.app.compactChannelSession(ctx, nil, lang)
	}
	return b.app.compactChannelSession(ctx, session, lang)
}

// NewSession resets one chat's conversation.
func (b *whatsappBridge) NewSession(request channel.Request) {
	session, err := b.freshSession(request)
	if err != nil {
		return
	}
	session.SetTodos(nil)
	_ = session.Save()
}

func (b *whatsappBridge) Stop() { b.app.Stop() }

func (b *whatsappBridge) Model() string { return b.app.ModelLabel() }

func (b *whatsappBridge) SetModel(query string) (string, error) { return b.app.SetModel(query) }

func (b *whatsappBridge) Models() string { return b.app.AvailableModelsText("en") }

func (b *whatsappBridge) ModelsFor(lang string) string { return b.app.AvailableModelsText(lang) }

func (b *whatsappBridge) GitDiff(ctx context.Context) (string, error) {
	diff, err := b.app.GitDiff(ctx)
	if err != nil {
		return "", err
	}
	return b.app.RedactForChannel(diff), nil
}

func (b *whatsappBridge) Status() string { return b.app.Status() }

func (b *whatsappBridge) Workspace() string {
	return b.app.AgentWorkspace(b.account.AgentName(b.name))
}

func (b *whatsappBridge) session(request channel.Request) (*agent.Session, error) {
	agentName := b.account.AgentName(b.name)
	dir, err := agenthome.SessionsDirFor(agentName)
	if err != nil {
		return nil, err
	}
	id := channelSessionKey(config.ChannelWhatsApp, b.name, b.account, request)
	if loaded, err := agent.LoadSessionIn(dir, id); err == nil {
		loaded.SetAgent(agentName)
		loaded.SetGuest(!request.Admin)
		return loaded, nil
	}
	session, err := agent.NewSessionWithID(agentName, id, b.app.AgentWorkspace(agentName), b.app.CurrentModel().ID)
	if err != nil {
		return nil, err
	}
	session.SetGuest(!request.Admin)
	session.SetStoreDir(dir)
	return session, nil
}

func (b *whatsappBridge) freshSession(request channel.Request) (*agent.Session, error) {
	agentName := b.account.AgentName(b.name)
	dir, err := agenthome.SessionsDirFor(agentName)
	if err != nil {
		return nil, err
	}
	id := channelSessionKey(config.ChannelWhatsApp, b.name, b.account, request)
	session, err := agent.NewSessionWithID(agentName, id, b.app.AgentWorkspace(agentName), b.app.CurrentModel().ID)
	if err != nil {
		return nil, err
	}
	session.SetGuest(!request.Admin)
	session.SetStoreDir(dir)
	return session, nil
}

// limits bounds a guest turn with the account's own ceilings.
func (b *whatsappBridge) limits(request channel.Request) (bool, int, float64) {
	if request.Admin {
		return false, 0, 0
	}
	limits := b.account.EffectiveGuestLimits()
	return true, limits.MaxSteps, limits.CostBudgetUSD
}
