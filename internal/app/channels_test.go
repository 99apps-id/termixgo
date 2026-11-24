package app

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agenthome"
	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/telegram"
)

func TestChannelSessionIDIsStableAndSafe(t *testing.T) {
	first := channelSessionID(config.ChannelTelegram, "sales", "-1001234567890")
	again := channelSessionID(config.ChannelTelegram, "sales", "-1001234567890")
	if first != again {
		t.Errorf("the same chat must map to the same session id: %q vs %q", first, again)
	}
	if strings.ContainsAny(first, `/\\.`) {
		t.Errorf("a session id may not carry a separator or dot: %q", first)
	}
	other := channelSessionID(config.ChannelTelegram, "sales", "42")
	if other == first {
		t.Errorf("two chats must not share one session id")
	}
	if got := channelSessionID(config.ChannelWhatsApp, "sales", "628123@s.whatsapp.net"); strings.ContainsAny(got, `/\\.`) {
		t.Errorf("a WhatsApp JID must be folded onto a safe id: %q", got)
	}
}

func TestBridgeKeepsOneSessionPerChat(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	paths, err := agenthome.Ensure("sales", "", "assistant")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg = cfg.Trust(paths.Workspace).WithAgent("sales", config.Agent{})
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bridge := &telegramBridge{
		app:     application,
		name:    "sales",
		account: config.TelegramAccount{ChannelAccount: config.ChannelAccount{Agent: "sales", Enabled: true}},
	}

	request := channel.Request{Channel: config.ChannelTelegram, Account: "sales", ChatID: "12345"}
	first, err := bridge.session(request)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	first.AddUser("halo")
	if err := first.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	again, err := bridge.session(request)
	if err != nil {
		t.Fatalf("session again: %v", err)
	}
	if again.ID() != first.ID() {
		t.Errorf("the same chat must reload the same session: %q vs %q", again.ID(), first.ID())
	}
	if again.MessageCount() == 0 {
		t.Errorf("the saved conversation was not reloaded")
	}
	if again.Agent() != "sales" {
		t.Errorf("session agent = %q, want sales", again.Agent())
	}
	if !strings.HasPrefix(first.StoreDir(), application.AgentHome("sales")) {
		t.Errorf("a channel session must live in the agent home: %q", first.StoreDir())
	}

	other, err := bridge.session(channel.Request{Channel: config.ChannelTelegram, Account: "sales", ChatID: "999"})
	if err != nil {
		t.Fatalf("other session: %v", err)
	}
	if other.ID() == first.ID() {
		t.Errorf("two chats must not share one session")
	}

	// /new rewrites the same chat's session empty rather than starting another
	// chat's conversation.
	bridge.NewSession(request)
	fresh, err := bridge.session(request)
	if err != nil {
		t.Fatalf("session after reset: %v", err)
	}
	if fresh.ID() != first.ID() || fresh.MessageCount() != 0 {
		t.Errorf("reset session = %q with %d messages", fresh.ID(), fresh.MessageCount())
	}
}

func TestChannelStatusesListsConfiguredAccounts(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg = cfg.WithTelegramAccount("zeta", config.TelegramAccount{ChannelAccount: config.ChannelAccount{Enabled: true}})
	cfg = cfg.WithTelegramAccount("alpha", config.TelegramAccount{ChannelAccount: config.ChannelAccount{Enabled: false}})
	cfg = cfg.WithWhatsAppAccount("sales", config.ChannelAccount{Enabled: true})
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	statuses := application.ChannelStatuses()
	if len(statuses) != 3 {
		t.Fatalf("statuses = %+v", statuses)
	}
	if statuses[0].Channel != config.ChannelTelegram || statuses[0].Account != "alpha" {
		t.Errorf("statuses must be sorted by channel then account: %+v", statuses)
	}
	if statuses[0].State != "disabled" {
		t.Errorf("a disabled account reads %q", statuses[0].State)
	}
	if statuses[1].Account != "zeta" || statuses[1].State != "no token" {
		t.Errorf("an enabled account without a token reads %+v", statuses[1])
	}
	if statuses[2].Channel != config.ChannelWhatsApp {
		t.Errorf("the WhatsApp account is missing: %+v", statuses)
	}
}

func TestStartChannelRefusesUnknownAccounts(t *testing.T) {
	application := newTestApp(t)
	if err := application.StartChannel(config.ChannelTelegram, "ghost"); err == nil {
		t.Errorf("an unknown account must be refused")
	}
	if err := application.StartChannel(config.ChannelWhatsApp, "sales"); err == nil {
		t.Errorf("an unknown WhatsApp account must be refused")
	} else if !strings.Contains(err.Error(), "no whatsapp account") {
		t.Errorf("the refusal must name the account: %v", err)
	}
}

func TestTelegramBridgeGuestHasNoApprovalInteractor(t *testing.T) {
	application := newTestApp(t)
	bridge := &telegramBridge{
		app:     application,
		name:    "default",
		account: config.TelegramAccount{ChannelAccount: config.ChannelAccount{Enabled: true}},
		bot:     telegram.New("123:abc", nil),
	}
	if bridge.interactorFor(channel.Request{ChatID: "42", Admin: false}) != nil {
		t.Errorf("guest requests must not receive an approval interactor")
	}
	if bridge.interactorFor(channel.Request{ChatID: "42", Admin: true}) == nil {
		t.Errorf("admin requests must receive an approval interactor")
	}
}

func TestRunChannelTurnStreamDoesNotDuplicateEvents(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()

	bridge := &telegramBridge{
		app:     application,
		name:    "default",
		account: config.TelegramAccount{ChannelAccount: config.ChannelAccount{Enabled: true}},
	}
	req := channel.Request{Channel: config.ChannelTelegram, Account: "default", ChatID: "77", Admin: true}
	var events []telegram.StreamEvent
	answer, err := bridge.RunPromptStream(t.Context(), req, "hello", func(ev telegram.StreamEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("RunPromptStream: %v", err)
	}
	if answer != "ok" {
		t.Fatalf("answer = %q, want ok", answer)
	}
	textEvents := 0
	for _, ev := range events {
		if ev.Kind == "text" {
			textEvents++
		}
	}
	if textEvents != 1 {
		t.Errorf("expected 1 text stream event (no duplicate observer emission), got %d (%+v)", textEvents, events)
	}
}

// An operator and a guest in one chat must never read each other's history.
func TestChannelSessionsAreSeparatedByRole(t *testing.T) {
	admin := channelSessionIDFor(config.ChannelTelegram, "sales", channel.Request{ChatID: "-100", Admin: true})
	guest := channelSessionIDFor(config.ChannelTelegram, "sales", channel.Request{ChatID: "-100"})
	if admin == guest {
		t.Fatalf("operator and guest share the session %q", admin)
	}
	if admin != channelSessionID(config.ChannelTelegram, "sales", "-100") {
		t.Errorf("the operator's session id must stay what it was, so saved history is kept: %q", admin)
	}
	if !strings.HasSuffix(guest, "-guest") {
		t.Errorf("guest session = %q", guest)
	}
}
