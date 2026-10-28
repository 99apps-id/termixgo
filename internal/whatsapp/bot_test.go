package whatsapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
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
	replyToBot := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String("setuju"),
			ContextInfo: &waE2E.ContextInfo{
				Participant:   proto.String("628111@s.whatsapp.net"),
				QuotedMessage: &waE2E.Message{Conversation: proto.String("jawaban bot")},
			},
		},
	}
	if !bot.mentionsSelf(replyToBot) {
		t.Errorf("a reply quoting the bot must count as a mention")
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

type fakeImageAgent struct {
	fakeAgent
	imagePrompt    string
	imageMediaType string
	imageData      string
}

func (a *fakeImageAgent) RunPromptWithImage(ctx context.Context, req channel.Request, prompt, mediaType, data string, progress func(string)) (string, error) {
	a.imagePrompt = prompt
	a.imageMediaType = mediaType
	a.imageData = data
	return "vision analyzed: **success**", nil
}

func TestFakeImageAgent(t *testing.T) {
	agent := &fakeImageAgent{}
	ans, err := agent.RunPromptWithImage(context.Background(), channel.Request{}, "desc", "image/png", "abc", nil)
	if err != nil || !strings.Contains(ans, "vision analyzed") {
		t.Fatalf("unexpected fakeImageAgent output: %v, %v", ans, err)
	}
	if agent.imagePrompt != "desc" || agent.imageMediaType != "image/png" || agent.imageData != "abc" {
		t.Fatalf("unexpected recorded image params: %+v", agent)
	}
}

func TestNaturalErrorAuthFailure(t *testing.T) {
	authErr := errors.New("loadCodeAssist returned 401: Request had invalid authentication credentials")
	idMsg := naturalError(authErr, "id")
	if !strings.Contains(idMsg, "autentikasi") {
		t.Errorf("expected Indonesian auth failure message, got: %q", idMsg)
	}

	enMsg := naturalError(authErr, "en")
	if !strings.Contains(enMsg, "authentication issue") {
		t.Errorf("expected English auth failure message, got: %q", enMsg)
	}
}

func TestReplyFormatsEnrichedMarkdown(t *testing.T) {
	bot := &Bot{}
	var lastReply string
	bot.sendHook = func(chatID, text string) {
		lastReply = text
	}

	ctx := context.Background()
	bot.reply(ctx, "628123@s.whatsapp.net", "# Ringkasan\nBerikut adalah **hasil penting**.")
	if strings.Contains(lastReply, "**") {
		t.Errorf("reply must not contain double asterisks, got: %q", lastReply)
	}
	if !strings.Contains(lastReply, "*Ringkasan*") || !strings.Contains(lastReply, "*hasil penting*") {
		t.Errorf("reply missing expected formatted markdown: %q", lastReply)
	}
}

func TestUnwrapMessageAndExtensions(t *testing.T) {
	if unwrapMessage(nil) != nil {
		t.Errorf("nil message unwrap should be nil")
	}

	inner := &waE2E.Message{Conversation: proto.String("wrapped")}
	ephem := &waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: inner}}
	if got := unwrapMessage(ephem); got.GetConversation() != "wrapped" {
		t.Errorf("ephemeral message unwrap failed: %v", got)
	}

	vo := &waE2E.Message{ViewOnceMessage: &waE2E.FutureProofMessage{Message: inner}}
	if got := unwrapMessage(vo); got.GetConversation() != "wrapped" {
		t.Errorf("viewOnce message unwrap failed: %v", got)
	}

	vo2 := &waE2E.Message{ViewOnceMessageV2: &waE2E.FutureProofMessage{Message: inner}}
	if got := unwrapMessage(vo2); got.GetConversation() != "wrapped" {
		t.Errorf("viewOnceV2 message unwrap failed: %v", got)
	}

	docCap := &waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: inner}}
	if got := unwrapMessage(docCap); got.GetConversation() != "wrapped" {
		t.Errorf("docWithCaption message unwrap failed: %v", got)
	}

	if !isImageExtension("foto.png") || !isImageExtension("pic.jpg") || !isImageExtension("img.WEBP") {
		t.Errorf("image extension check failed for valid images")
	}
	if isImageExtension("doc.pdf") || isImageExtension("text.txt") {
		t.Errorf("image extension check incorrectly matched non-images")
	}

	if !isPDFVisionModel("antigravity-gemini-3.8-flash") || !isPDFVisionModel("gemini-2.5-pro") || !isPDFVisionModel("claude-3-7-sonnet") {
		t.Errorf("isPDFVisionModel should match gemini and claude models")
	}
	if isPDFVisionModel("step-3.7-flash") || isPDFVisionModel("deepseek-v3") {
		t.Errorf("isPDFVisionModel should not match stepfun or deepseek")
	}

	if !isAuthError(errors.New("401 Unauthorized")) || !isAuthError(errors.New("invalid authentication token")) {
		t.Errorf("isAuthError should match 401 or invalid auth")
	}
	if isAuthError(errors.New("connection timeout")) || isAuthError(nil) {
		t.Errorf("isAuthError should not match connection timeout")
	}
}

func TestMediaHandlersGracefulNilClient(t *testing.T) {
	var replies []string
	bot := &Bot{}
	bot.sendHook = func(chatID, text string) {
		replies = append(replies, text)
	}

	ctx := context.Background()
	req := channel.Request{ChatID: "628123@s.whatsapp.net", SenderID: "628123@s.whatsapp.net"}
	policy := config.ChannelAccount{Enabled: true}

	imgMsg := &waE2E.ImageMessage{Caption: proto.String("cek foto")}
	bot.runImage(ctx, req, imgMsg, "", "id", policy)
	if len(replies) == 0 {
		t.Errorf("expected error reply for runImage with nil client")
	}

	replies = nil
	docMsg := &waE2E.DocumentMessage{FileName: proto.String("laporan.pdf")}
	bot.runDocument(ctx, req, docMsg, "", "id", policy)
	if len(replies) == 0 {
		t.Errorf("expected error reply for runDocument with nil client")
	}
}

func TestSendToAndPairingStore(t *testing.T) {
	bot := &Bot{}
	if err := bot.SendTo(context.Background(), "invalid-jid", "halo"); err == nil {
		t.Errorf("SendTo should fail for invalid jid")
	}

	pairing, _ := channel.OpenPairingStore(t.TempDir() + "/pair.json")
	policy := config.ChannelAccount{Enabled: true, DMPolicy: config.DMPolicyPairing}
	bot.SetAccount("test", policy, pairing)

	req := channel.Request{Account: "test", SenderID: "628999@s.whatsapp.net", SenderName: "User"}
	if !bot.mayPair(policy, req) {
		t.Errorf("mayPair should be true for DM with DMPolicyPairing")
	}
	prompt := bot.pairingPrompt(req)
	if !strings.Contains(prompt, "Sandi pairing") {
		t.Errorf("pairing prompt missing code instructions: %q", prompt)
	}
}

type voiceAndInspectorAgent struct {
	fakeAgent
	transcript string
	cost       string
	plan       string
	compact    string
}

func (a *voiceAndInspectorAgent) TranscribeAudio(ctx context.Context, data []byte, filename string) (string, error) {
	return a.transcript, nil
}

func (a *voiceAndInspectorAgent) RunVoicePrompt(ctx context.Context, req channel.Request, prompt string, progress func(string)) (string, error) {
	a.prompts = append(a.prompts, prompt)
	return "voice-reply", nil
}

func (a *voiceAndInspectorAgent) CostInfo(req channel.Request, lang string) string {
	return a.cost
}

func (a *voiceAndInspectorAgent) PlanInfo(req channel.Request, lang string) string {
	return a.plan
}

func (a *voiceAndInspectorAgent) CompactSession(ctx context.Context, req channel.Request, lang string) string {
	return a.compact
}

func TestEphemeralGroupMentionCommandAndQuotedContext(t *testing.T) {
	agent := &voiceAndInspectorAgent{
		fakeAgent:  fakeAgent{model: "step-3.5-flash", status: "ok-status"},
		transcript: "halo dari voice note",
		cost:       "Total token: 120",
		plan:       "Rencana (1/1 selesai)",
		compact:    "Riwayat percakapan berhasil diringkas",
	}
	var replies []string
	bot := New(nil, agent)
	bot.sendHook = func(chatID, text string) {
		replies = append(replies, text)
	}
	bot.downloadHook = func(ctx context.Context, msg whatsmeow.DownloadableMessage) ([]byte, error) {
		return []byte("fake-audio"), nil
	}
	bot.selfIDs = map[string]bool{channel.CanonicalID("628111@s.whatsapp.net"): true}
	bot.SetAccount("default", config.ChannelAccount{
		Enabled:     true,
		Admins:      []string{"628999@s.whatsapp.net"},
		GroupPolicy: config.GroupPolicyMention,
	}, nil)

	// 1. Ephemeral message in group with @bot /cost command
	ephemCmd := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:    types.NewJID("12345", types.GroupServer),
				Sender:  types.NewJID("628999", types.DefaultUserServer),
				IsGroup: true,
			},
		},
		Message: &waE2E.Message{
			EphemeralMessage: &waE2E.FutureProofMessage{
				Message: &waE2E.Message{
					ExtendedTextMessage: &waE2E.ExtendedTextMessage{
						Text: proto.String("@628111 /cost"),
						ContextInfo: &waE2E.ContextInfo{
							MentionedJID: []string{"628111@s.whatsapp.net"},
						},
					},
				},
			},
		},
	}
	bot.handleMessage(context.Background(), ephemCmd)
	if len(replies) == 0 || !strings.Contains(replies[len(replies)-1], "Total token: 120") {
		t.Fatalf("expected /cost reply from ephemeral group mention, got %v", replies)
	}

	// 2. Quoted message reply context + /plan and /compact
	req := channel.Request{ChatID: "628999@s.whatsapp.net", SenderID: "628999@s.whatsapp.net", Text: "/plan", Admin: true}
	bot.handleCommand(context.Background(), req)
	if !strings.Contains(replies[len(replies)-1], "Rencana (1/1 selesai)") {
		t.Errorf("unexpected /plan reply: %v", replies[len(replies)-1])
	}
	req.Text = "/compact"
	bot.handleCommand(context.Background(), req)
	if !strings.Contains(replies[len(replies)-1], "berhasil diringkas") {
		t.Errorf("unexpected /compact reply: %v", replies[len(replies)-1])
	}

	// 3. Voice note with quoted message context
	audioEvt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.NewJID("628999", types.DefaultUserServer),
				Sender: types.NewJID("628999", types.DefaultUserServer),
			},
		},
		Message: &waE2E.Message{
			AudioMessage: &waE2E.AudioMessage{
				Mimetype: proto.String("audio/ogg; codecs=opus"),
				PTT:      proto.Bool(true),
				ContextInfo: &waE2E.ContextInfo{
					QuotedMessage: &waE2E.Message{
						Conversation: proto.String("pesan sebelumnya"),
					},
				},
			},
		},
	}
	bot.handleMessage(context.Background(), audioEvt)
	if len(agent.prompts) == 0 || !strings.Contains(agent.prompts[len(agent.prompts)-1], "pesan sebelumnya") || !strings.Contains(agent.prompts[len(agent.prompts)-1], "halo dari voice note") {
		t.Fatalf("expected voice note prompt with quoted context, got %v", agent.prompts)
	}
	if replies[len(replies)-1] != "voice-reply" {
		t.Errorf("expected voice-reply, got %q", replies[len(replies)-1])
	}
}

func TestNormalizeJIDAndSendFileTo(t *testing.T) {
	if got := NormalizeJID("0821-1348-7323"); got != "6282113487323@s.whatsapp.net" {
		t.Fatalf("NormalizeJID(0821-1348-7323) = %q, want 6282113487323@s.whatsapp.net", got)
	}
	if got := NormalizeJID("6282113487323"); got != "6282113487323@s.whatsapp.net" {
		t.Fatalf("NormalizeJID(6282113487323) = %q, want 6282113487323@s.whatsapp.net", got)
	}

	bot := New(nil, &fakeAgent{})
	var gotChat, gotName, gotCaption string
	var gotBytes []byte
	bot.sendFileHook = func(chatID, filename string, data []byte, caption string) {
		gotChat = chatID
		gotName = filename
		gotBytes = data
		gotCaption = caption
	}
	if !bot.Connected() {
		t.Fatal("expected bot with sendFileHook to report Connected() == true")
	}
	err := bot.SendFileTo(context.Background(), "082113487323", "dir/laporan.pdf", []byte("pdf-data"), "**Laporan** penting")
	if err != nil {
		t.Fatalf("SendFileTo: %v", err)
	}
	if gotChat != "6282113487323@s.whatsapp.net" || gotName != "laporan.pdf" || string(gotBytes) != "pdf-data" || gotCaption != "*Laporan* penting" {
		t.Fatalf("unexpected SendFileTo capture: chat=%q name=%q data=%q caption=%q", gotChat, gotName, string(gotBytes), gotCaption)
	}
}
