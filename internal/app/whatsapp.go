package app

import (
	"context"
	"errors"
	"fmt"
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
		// Report the live state: without this the status view kept showing
		// "connecting" forever on a healthy account whose bot never reports
		// back, and a logout that Run only logs left the same stale line.
		a.setChannelStatus(key, "connected")
		if err := bot.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			a.setChannelStatus(key, "stopped: "+err.Error())
		}
		a.dropChannelRuntime(key)
	}()
	return nil
}

// whatsappBridge is the app side of one WhatsApp account. Sessions are per
// chat, under the owning agent's home, and the turn runs through the same
// runner as every other surface.
type whatsappBridge struct {
	app     *App
	name    string
	account config.ChannelAccount
	send    func(ctx context.Context, chatID, text string) error

	// pendingApproval is the one in-chat approval waiting for an answer. The
	// run slot is single, so at most one turn blocks on approval at a time;
	// approvalChat is the chat whose operator may answer it.
	mu              sync.Mutex
	pendingApproval chan agent.Decision
	approvalChat    string
	approvalSender  string
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
	return w.bridge.askApproval(w.request, request)
}

func (w *whatsAppApprover) Ask(string, []string) (string, error) {
	return "", errors.New("interactive questions are not supported over WhatsApp yet")
}

// askApproval posts the request in the chat and waits for the operator's
// reply. No answer inside the window is a denial, which is the safe default.
func (b *whatsappBridge) askApproval(request channel.Request, approval agent.ApprovalRequest) agent.Decision {
	if b.send == nil {
		return agent.DecisionDeny
	}
	lang := whatsapp.SenderLanguage(request)
	if err := b.send(context.Background(), request.ChatID, approvalPromptText(lang, approval)); err != nil {
		return agent.DecisionDeny
	}
	answer := make(chan agent.Decision, 1)
	b.mu.Lock()
	b.pendingApproval = answer
	b.approvalChat = request.ChatID
	b.approvalSender = request.SenderID
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.pendingApproval == answer {
			b.pendingApproval = nil
			b.approvalChat = ""
			b.approvalSender = ""
		}
		b.mu.Unlock()
	}()
	timer := time.NewTimer(approvalTimeout)
	defer timer.Stop()
	select {
	case decision := <-answer:
		return decision
	case <-timer.C:
		_ = b.send(context.Background(), request.ChatID, approvalTimeoutText(lang))
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
	b.mu.Lock()
	if b.pendingApproval == nil || channel.CanonicalID(b.approvalChat) != channel.CanonicalID(request.ChatID) {
		b.mu.Unlock()
		return false
	}
	if !request.Admin && (b.approvalSender == "" || channel.CanonicalID(b.approvalSender) != channel.CanonicalID(request.SenderID)) {
		b.mu.Unlock()
		return false
	}
	answer := b.pendingApproval
	b.pendingApproval = nil
	b.approvalChat = ""
	b.approvalSender = ""
	b.mu.Unlock()

	decision := mapApprovalAnswer(trimmed)
	_ = b.send(context.Background(), request.ChatID, approvalResultText(whatsapp.SenderLanguage(request), decision))
	select {
	case answer <- decision:
	default:
	}
	return true
}

// HasPendingApproval reports whether an approval is actively waiting in request's chat.
func (b *whatsappBridge) HasPendingApproval(request channel.Request) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pendingApproval != nil && channel.CanonicalID(b.approvalChat) == channel.CanonicalID(request.ChatID)
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

// approvalPromptText renders the in-chat request. WhatsApp carries no inline
// buttons in this build, so the operator answers with a key.
func approvalPromptText(lang string, request agent.ApprovalRequest) string {
	detail := agent.Shorten(request.Detail, 160)
	if lang == "id" {
		return fmt.Sprintf("Perlu persetujuan: %s (%s)\n%s\nBalas y (sekali), s (sesi), a (selalu), n (tolak). %d menit.",
			request.Tool, request.Risk, detail, int(approvalTimeout.Minutes()))
	}
	return fmt.Sprintf("Approval needed: %s (%s)\n%s\nReply y (once), s (session), a (always), n (deny). %d minutes.",
		request.Tool, request.Risk, detail, int(approvalTimeout.Minutes()))
}

func approvalTimeoutText(lang string) string {
	if lang == "id" {
		return "Persetujuan habis waktu, ditolak."
	}
	return "Approval timed out, denied."
}

func approvalResultText(lang string, decision agent.Decision) string {
	switch decision {
	case agent.DecisionAllowOnce:
		if lang == "id" {
			return "Diizinkan sekali."
		}
		return "Allowed once."
	case agent.DecisionAllowSession:
		if lang == "id" {
			return "Diizinkan untuk sesi ini."
		}
		return "Allowed for this session."
	case agent.DecisionAllowAlways:
		if lang == "id" {
			return "Selalu diizinkan."
		}
		return "Always allowed."
	default:
		if lang == "id" {
			return "Ditolak."
		}
		return "Denied."
	}
}

// RunPrompt runs one turn for one chat.
func (b *whatsappBridge) RunPrompt(ctx context.Context, request channel.Request, prompt string, progress func(string)) (string, error) {
	session, err := b.session(request)
	if err != nil {
		return "", err
	}
	guest, maxSteps, budget := b.limits(request)
	answer, err := b.app.runChannelTurn(ctx, b.account.AgentName(b.name), session, b.interactorFor(request), b.promptFor(request, prompt), nil, guest, maxSteps, budget, progress)
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
	session, err := b.session(request)
	if err != nil {
		return "", err
	}
	images := []provider.Image{{MediaType: mediaType, Data: data}}
	guest, maxSteps, budget := b.limits(request)
	answer, err := b.app.runChannelTurn(ctx, b.account.AgentName(b.name), session, b.interactorFor(request), b.promptFor(request, prompt), images, guest, maxSteps, budget, progress)
	if errors.Is(err, ErrBusy) {
		return "", channel.ErrBusy
	}
	if err != nil {
		return "", err
	}
	return b.app.RedactForChannel(answer), nil
}

// TranscribeAudio forwards voice/audio bytes to the app's configured STT engine.
func (b *whatsappBridge) TranscribeAudio(ctx context.Context, data []byte, filename string) (string, error) {
	return b.app.TranscribeAudio(ctx, data, filename)
}

// RunVoicePrompt runs a voice-originated turn for one chat, falling back to the
// configured voice model when the primary model fails.
func (b *whatsappBridge) RunVoicePrompt(ctx context.Context, request channel.Request, prompt string, progress func(string)) (string, error) {
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
	id := channelSessionID(config.ChannelWhatsApp, b.name, request.ChatID)
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

func (b *whatsappBridge) freshSession(request channel.Request) (*agent.Session, error) {
	agentName := b.account.AgentName(b.name)
	dir, err := agenthome.SessionsDirFor(agentName)
	if err != nil {
		return nil, err
	}
	id := channelSessionID(config.ChannelWhatsApp, b.name, request.ChatID)
	session, err := agent.NewSessionWithID(agentName, id, b.app.AgentWorkspace(agentName), b.app.CurrentModel().ID)
	if err != nil {
		return nil, err
	}
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
