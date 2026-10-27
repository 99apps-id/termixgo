package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/agenthome"
	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/whatsapp"
)

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
	bot.SetAccount(name, entry, a.pairingStore())

	key := channel.AccountID{Channel: config.ChannelWhatsApp, Account: name}
	bot.SetLogger(func(line string) {
		a.setChannelStatus(key, line)
	})

	ctx, cancel := context.WithCancel(context.Background())
	runtime := &channelRuntime{id: key, cancel: cancel, status: "connecting"}
	runtime.send = bot.SendTo
	a.mu.Lock()
	if a.channels == nil {
		a.channels = map[channel.AccountID]*channelRuntime{}
	}
	a.channels[key] = runtime
	a.mu.Unlock()

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
}

// RunPrompt runs one turn for one chat. Progress is ignored on purpose: the
// transport shows a typing presence, and the chat receives only the answer.
func (b *whatsappBridge) RunPrompt(ctx context.Context, request channel.Request, prompt string, _ func(string)) (string, error) {
	session, err := b.session(request)
	if err != nil {
		return "", err
	}
	guest, maxSteps, budget := b.limits(request)
	answer, err := b.app.runChannelTurn(ctx, b.account.AgentName(b.name), session, nil, b.promptFor(request, prompt), nil, guest, maxSteps, budget, nil)
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
func (b *whatsappBridge) RunPromptWithImage(ctx context.Context, request channel.Request, prompt, mediaType, data string, _ func(string)) (string, error) {
	session, err := b.session(request)
	if err != nil {
		return "", err
	}
	images := []provider.Image{{MediaType: mediaType, Data: data}}
	guest, maxSteps, budget := b.limits(request)
	answer, err := b.app.runChannelTurn(ctx, b.account.AgentName(b.name), session, nil, b.promptFor(request, prompt), images, guest, maxSteps, budget, nil)
	if errors.Is(err, ErrBusy) {
		return "", channel.ErrBusy
	}
	if err != nil {
		return "", err
	}
	return b.app.RedactForChannel(answer), nil
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

func (b *whatsappBridge) Models() string { return b.app.AvailableModelsText() }

func (b *whatsappBridge) GitDiff(ctx context.Context) (string, error) { return b.app.GitDiff(ctx) }

func (b *whatsappBridge) Status() string { return b.app.Status() }

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
