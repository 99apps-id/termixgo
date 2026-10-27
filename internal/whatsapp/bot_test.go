package whatsapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
)

func testEvent(text string, mentioned ...string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.NewJID("628123", types.DefaultUserServer),
				Sender: types.NewJID("628999", types.DefaultUserServer),
			},
			PushName: "Budi",
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String(text),
				ContextInfo: &waE2E.ContextInfo{
					MentionedJID: mentioned,
				},
			},
		},
	}
}

func TestMessageText(t *testing.T) {
	if got := messageText(&waE2E.Message{Conversation: proto.String("halo")}); got != "halo" {
		t.Errorf("conversation text = %q", got)
	}
	if got := messageText(nil); got != "" {
		t.Errorf("a nil message has no text, got %q", got)
	}
	image := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("foto")}}
	if got := messageText(image); got != "" {
		t.Errorf("an image has no text to run, got %q", got)
	}
}

func TestRequestFromNormalizesTheMessage(t *testing.T) {
	bot := &Bot{}
	bot.SetAccount("sales", config.ChannelAccount{Enabled: true}, nil)
	event := testEvent("halo", "628111@s.whatsapp.net")
	event.Info.Chat = types.NewJID("12345-67890", types.GroupServer)
	event.Info.IsGroup = true

	request := bot.requestFrom(event)
	if request.Channel != config.ChannelWhatsApp || request.Account != "sales" {
		t.Errorf("request identity = %+v", request)
	}
	if !strings.HasSuffix(request.ChatID, "@g.us") || !request.IsGroup {
		t.Errorf("a group chat must be marked as one: %+v", request)
	}
	if request.SenderID != "628999@s.whatsapp.net" || request.SenderName != "Budi" {
		t.Errorf("sender = %q (%q)", request.SenderID, request.SenderName)
	}
	if request.Text != "halo" {
		t.Errorf("text = %q", request.Text)
	}
	if request.MentionsBot {
		t.Errorf("a mention of another number must not count")
	}
}

func TestMentionsSelfUsesTheLinkedJIDs(t *testing.T) {
	bot := &Bot{}
	bot.SetAccount("sales", config.ChannelAccount{Enabled: true}, nil)
	bot.selfIDs = map[string]bool{channel.CanonicalID("628111@s.whatsapp.net"): true}

	if !bot.mentionsSelf(testEvent("halo @628111", "628111@s.whatsapp.net").Message) {
		t.Errorf("a mention of the linked number must count")
	}
	if bot.mentionsSelf(testEvent("halo @628222", "628222@s.whatsapp.net").Message) {
		t.Errorf("a mention of another number must not count")
	}
	if bot.mentionsSelf(testEvent("halo").Message) {
		t.Errorf("a plain message must not count")
	}
}

func TestNaturalErrorHidesInternals(t *testing.T) {
	busy := naturalError(channel.ErrBusy)
	if !strings.Contains(busy, "Sebentar") {
		t.Errorf("a busy turn must be one friendly line, got %q", busy)
	}
	raw := errors.New("run_command failed: permission denied in /home/secret")
	message := naturalError(raw)
	if strings.Contains(message, "run_command") || strings.Contains(message, "/home/secret") {
		t.Errorf("an internal error leaked into the chat: %q", message)
	}
	if !strings.Contains(message, "gangguan") {
		t.Errorf("the generic failure must still read like a person: %q", message)
	}
	if got := naturalError(context.Canceled); !strings.Contains(got, "dibatalkan") {
		t.Errorf("a cancelled turn reads %q", got)
	}
}

type fakeAgent struct {
	model     string
	models    string
	status    string
	diff      string
	diffErr   error
	setErr    error
	newCalls  int
	stopCalls int
	prompts   []string
}

func (a *fakeAgent) RunPrompt(ctx context.Context, req channel.Request, prompt string, progress func(string)) (string, error) {
	a.prompts = append(a.prompts, prompt)
	return "ok", nil
}

func (a *fakeAgent) NewSession(req channel.Request) {
	a.newCalls++
}

func (a *fakeAgent) Stop() {
	a.stopCalls++
}

func (a *fakeAgent) Model() string {
	return a.model
}

func (a *fakeAgent) SetModel(query string) (string, error) {
	if a.setErr != nil {
		return "", a.setErr
	}
	a.model = query
	return query, nil
}

func (a *fakeAgent) Models() string {
	return a.models
}

func (a *fakeAgent) Status() string {
	return a.status
}

func (a *fakeAgent) GitDiff(ctx context.Context) (string, error) {
	return a.diff, a.diffErr
}

func TestWhatsAppSlashCommandsAdmin(t *testing.T) {
	agent := &fakeAgent{
		model:  "muse-spark-1.3",
		models: "Registered models list",
		status: "agent online",
		diff:   "diff --git a/test.txt",
	}
	var replies []string
	bot := New(nil, agent)
	bot.sendHook = func(chatID, text string) {
		replies = append(replies, text)
	}
	bot.SetAccount("default", config.ChannelAccount{
		Enabled: true,
		Admins:  []string{"628111@s.whatsapp.net"},
	}, nil)

	ctx := context.Background()

	// /models
	req := channel.Request{ChatID: "628111@s.whatsapp.net", SenderID: "628111@s.whatsapp.net", Text: "/models", Admin: true}
	bot.handleCommand(ctx, req)
	if len(replies) == 0 || replies[len(replies)-1] != "Registered models list" {
		t.Fatalf("unexpected /models reply: %v", replies)
	}

	// /model without arg
	req.Text = "/model"
	bot.handleCommand(ctx, req)
	if !strings.Contains(replies[len(replies)-1], "Model aktif saat ini: muse-spark-1.3") {
		t.Errorf("unexpected /model reply: %v", replies[len(replies)-1])
	}

	// /model stepfun
	req.Text = "/model stepfun"
	bot.handleCommand(ctx, req)
	if !strings.Contains(replies[len(replies)-1], "Model berhasil diubah ke: stepfun") {
		t.Errorf("unexpected /model stepfun reply: %v", replies[len(replies)-1])
	}
	if agent.model != "stepfun" {
		t.Errorf("expected model to be updated, got %s", agent.model)
	}

	// /status
	req.Text = "/status"
	bot.handleCommand(ctx, req)
	if replies[len(replies)-1] != "agent online" {
		t.Errorf("unexpected /status reply: %v", replies[len(replies)-1])
	}

	// /new
	req.Text = "/new"
	bot.handleCommand(ctx, req)
	if agent.newCalls != 1 {
		t.Errorf("expected 1 new session call, got %d", agent.newCalls)
	}

	// /stop
	req.Text = "/stop"
	bot.handleCommand(ctx, req)
	if agent.stopCalls != 1 {
		t.Errorf("expected 1 stop call, got %d", agent.stopCalls)
	}

	// /diff
	req.Text = "/diff"
	bot.handleCommand(ctx, req)
	if !strings.Contains(replies[len(replies)-1], "diff --git a/test.txt") {
		t.Errorf("unexpected /diff reply: %v", replies[len(replies)-1])
	}

	// /menu
	req.Text = "/menu"
	bot.handleCommand(ctx, req)
	if !strings.Contains(replies[len(replies)-1], "Daftar Perintah Slash Termixgo:") {
		t.Errorf("unexpected /menu reply: %v", replies[len(replies)-1])
	}
}

func TestWhatsAppSlashCommandsNonAdmin(t *testing.T) {
	agent := &fakeAgent{model: "muse-spark-1.3"}
	var replies []string
	bot := New(nil, agent)
	bot.sendHook = func(chatID, text string) {
		replies = append(replies, text)
	}
	bot.SetAccount("default", config.ChannelAccount{
		Enabled:  true,
		Admins:   []string{"628111@s.whatsapp.net"},
		DMPolicy: "open",
	}, nil)

	// Sender is not admin
	event := testEvent("/model stepfun")
	bot.handleMessage(context.Background(), event)
	if len(replies) == 0 || !strings.Contains(replies[0], "hanya untuk operator") {
		t.Fatalf("expected operator restriction, got: %v", replies)
	}
	if agent.model != "muse-spark-1.3" {
		t.Errorf("model should not have changed, got: %s", agent.model)
	}
}

func TestWhatsAppSlashCommandsForeignLanguage(t *testing.T) {
	agent := &fakeAgent{
		model:  "muse-spark-1.3",
		models: "Registered & Ready-to-Use Models",
		status: "agent online",
		diff:   "diff --git a/test.txt",
	}
	var replies []string
	bot := New(nil, agent)
	bot.sendHook = func(chatID, text string) {
		replies = append(replies, text)
	}
	bot.SetAccount("default", config.ChannelAccount{
		Enabled:  true,
		Admins:   []string{"14155552671@s.whatsapp.net"},
		DMPolicy: "open",
	}, nil)

	ctx := context.Background()

	// Foreign admin: /model
	req := channel.Request{ChatID: "14155552671@s.whatsapp.net", SenderID: "14155552671@s.whatsapp.net", Text: "/model", Admin: true}
	bot.handleCommand(ctx, req)
	if !strings.Contains(replies[len(replies)-1], "Current active model: muse-spark-1.3") {
		t.Errorf("unexpected foreign /model reply: %v", replies[len(replies)-1])
	}

	// /menu
	req.Text = "/menu"
	bot.handleCommand(ctx, req)
	if !strings.Contains(replies[len(replies)-1], "Termixgo Slash Commands:") {
		t.Errorf("unexpected foreign /menu reply: %v", replies[len(replies)-1])
	}

	// Non-admin foreign user
	replies = nil
	nonAdminEvent := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.NewJID("14155559999", types.DefaultUserServer),
				Sender: types.NewJID("14155559999", types.DefaultUserServer),
			},
			PushName: "John",
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String("/model stepfun"),
			},
		},
	}
	bot.handleMessage(ctx, nonAdminEvent)
	if len(replies) == 0 || !strings.Contains(replies[0], "restricted to the operator") {
		t.Fatalf("expected English operator restriction for foreign user, got: %v", replies)
	}
}
