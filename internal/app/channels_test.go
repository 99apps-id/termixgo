package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/agenthome"
	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/telegram"
)

func TestSlowClaimedOutboxDeliveryIsAcknowledged(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	key := channel.AccountID{Channel: config.ChannelWhatsApp, Account: "sales"}
	a := &App{channels: map[channel.AccountID]*channelRuntime{
		key: {send: func(context.Context, string, string) error {
			time.Sleep(3200 * time.Millisecond)
			return nil
		}},
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Millisecond)
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
	handled, err := a.deliverViaOutbox(ctx, key.Channel, key.Account, "42", "", nil, "hello")
	cancel()
	<-done
	if !handled || err != nil {
		t.Fatalf("slow delivery must be acknowledged once, handled=%v err=%v", handled, err)
	}
}

func TestGroupGuestConversationIdentity(t *testing.T) {
	for _, kind := range []string{config.ChannelTelegram, config.ChannelWhatsApp} {
		request := channel.Request{ChatID: "100", IsGroup: true, SenderID: "42"}
		first := channelSessionIDFor(kind, "sales", request)
		request.SenderID = "43"
		if first == channelSessionIDFor(kind, "sales", request) {
			t.Errorf("%s group guests share a conversation", kind)
		}
		request.IsGroup = false
		if got := channelSessionIDFor(kind, "sales", request); got != channelSessionID(kind, "sales", "100")+"-guest" {
			t.Errorf("%s private history moved to %s", kind, got)
		}
	}
}

// TestSharedSessionJoinsOperatorDMsAcrossChannels covers the opt-in shared
// history: two accounts naming the same sharedSession resolve their
// operator's 1:1 chats to one id, while guests, groups and unset accounts
// keep their own per-chat ids.
func TestSharedSessionJoinsOperatorDMsAcrossChannels(t *testing.T) {
	shared := config.ChannelAccount{SharedSession: "me"}
	admin := channel.Request{ChatID: "7", Admin: true}
	tg := channelSessionKey(config.ChannelTelegram, "default", shared, admin)
	waReq := channel.Request{ChatID: "628999@s.whatsapp.net", Admin: true}
	wa := channelSessionKey(config.ChannelWhatsApp, "work", shared, waReq)
	if tg != wa {
		t.Fatalf("operator DMs landed on %q and %q, want one shared conversation", tg, wa)
	}
	if strings.Contains(tg, "7") || strings.Contains(tg, "628999") {
		t.Errorf("shared id %q must not name one chat", tg)
	}
	guest := channelSessionKey(config.ChannelTelegram, "default", shared, channel.Request{ChatID: "7"})
	if guest == tg {
		t.Errorf("a guest joined the operator conversation %q", tg)
	}
	groupAdmin := channelSessionKey(config.ChannelTelegram, "default", shared, channel.Request{ChatID: "-100", Admin: true, IsGroup: true})
	if groupAdmin == tg {
		t.Errorf("a group chat joined the operator conversation %q", tg)
	}
	plain := channelSessionKey(config.ChannelTelegram, "default", config.ChannelAccount{}, admin)
	if plain != channelSessionID(config.ChannelTelegram, "default", "7") {
		t.Errorf("an account without sharedSession moved to %q", plain)
	}
	other := channelSessionKey(config.ChannelTelegram, "default", config.ChannelAccount{SharedSession: "other"}, admin)
	if other == tg {
		t.Errorf("a different shared name joined %q", tg)
	}
}

func TestRunningWhatsAppAccountSeesAdminChanges(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	admins := whatsAppAdmins("sales")
	for _, expected := range []string{"628123", "628456", ""} {
		account := config.ChannelAccount{Enabled: true}
		if expected != "" {
			account.Admins = []string{expected}
		}
		cfg = cfg.WithWhatsAppAccount("sales", account)
		if err := config.Save(cfg); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(admins(), ","); got != expected {
			t.Fatalf("runtime admins = %q, want %q", got, expected)
		}
	}
}

func TestGuestVoiceFallbackUsesChannelPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || !strings.Contains(string(body), "## GUEST SESSION") || !strings.Contains(string(body), "## MESSAGING CHANNEL RULES") {
			t.Error("fallback request is missing guest or channel policy")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Harga paket dasar adalah seratus ribu rupiah.\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	a := newTestApp(t)
	a.cfg.BaseURLs = map[string]string{"ollama": server.URL}
	a.cfg.VoiceModel = "ollama/test"
	b := &telegramBridge{app: a, name: "sales"}
	answer, err := b.RunVoicePrompt(t.Context(), channel.Request{ChatID: "42"}, "harga?", nil)
	if err != nil || !strings.Contains(answer, unverifiedNote) {
		t.Fatalf("expected an explicitly unverified sales answer, got %q, %v", answer, err)
	}
}

func TestBusyVoiceChatReturnsBusy(t *testing.T) {
	a := newTestApp(t)
	a.cfg.VoiceModel = "unknown-fallback"
	b := &whatsappBridge{app: a, name: "sales"}
	req := channel.Request{ChatID: "42"}
	id := channelSessionIDFor(config.ChannelWhatsApp, b.name, req)
	a.activeRuns = map[string]*activeRun{id: {}}
	if _, err := b.RunVoicePrompt(t.Context(), req, "hello", nil); !errors.Is(err, channel.ErrBusy) {
		t.Fatalf("busy voice chat returned %v", err)
	}
}

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
