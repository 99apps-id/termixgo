package whatsapp

import (
	"context"
	"strings"
	"sync"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
)

// routingAgent runs turns like the real bridge and answers pending approvals
// the way whatsappBridge.ResolveApproval does: only while one is waiting,
// never for a command.
type routingAgent struct {
	fakeAgent
	mu       sync.Mutex
	pending  bool
	resolved []string
}

func (a *routingAgent) ResolveApproval(request channel.Request, text string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.pending || strings.HasPrefix(strings.TrimSpace(text), "/") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "y", "yes", "ya", "iya", "allow", "ok", "1",
		"s", "session", "sesi", "2",
		"a", "always", "selalu", "3",
		"n", "no", "deny", "tidak", "tolak", "4":
		a.resolved = append(a.resolved, text)
		a.pending = false
		return true
	default:
		return false
	}
}

func (a *routingAgent) HasPendingApproval(request channel.Request) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pending
}

func (a *routingAgent) setPending(pending bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending = pending
}

func (a *routingAgent) answers() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.resolved...)
}

func adminApprovalEvent(text string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.NewJID("628111", types.DefaultUserServer),
				Sender: types.NewJID("628111", types.DefaultUserServer),
			},
			PushName: "Operator",
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String(text),
			},
		},
	}
}

func newApprovalBot(agent *routingAgent) (*Bot, *[]string) {
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
	return bot, &replies
}

func TestWhatsAppReplyAnswersThePendingApproval(t *testing.T) {
	agent := &routingAgent{}
	agent.setPending(true)
	bot, replies := newApprovalBot(agent)

	bot.handleMessage(context.Background(), adminApprovalEvent("a"))

	if got := agent.answers(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("the operator's reply must answer the approval, got %v", got)
	}
	if len(agent.prompts) != 0 {
		t.Errorf("an answered approval must not start a turn, got prompts %v", agent.prompts)
	}
	if len(*replies) != 0 {
		t.Errorf("the bot itself sends nothing here, got %v", *replies)
	}
}

func TestWhatsAppTextRunsATurnWhenNoApprovalWaits(t *testing.T) {
	agent := &routingAgent{fakeAgent: fakeAgent{model: "muse-spark-1.3"}}
	agent.setPending(false)
	bot, _ := newApprovalBot(agent)

	bot.handleMessage(context.Background(), adminApprovalEvent("halo"))

	if len(agent.prompts) != 1 || agent.prompts[0] != "halo" {
		t.Fatalf("ordinary text must run a turn, got %v", agent.prompts)
	}
}

func TestWhatsAppCommandStillRunsWhileApprovalWaits(t *testing.T) {
	agent := &routingAgent{fakeAgent: fakeAgent{status: "agent online"}}
	agent.setPending(true)
	bot, replies := newApprovalBot(agent)

	bot.handleMessage(context.Background(), adminApprovalEvent("/status"))

	if len(agent.answers()) != 0 {
		t.Errorf("a command must not be read as an approval answer, got %v", agent.answers())
	}
	if len(*replies) == 0 || !strings.Contains((*replies)[0], "agent online") {
		t.Fatalf("the command must still run, got %v", *replies)
	}
}

func TestWhatsAppPendingApprovalBlocksPromptDeadlock(t *testing.T) {
	agent := &routingAgent{}
	agent.setPending(true)
	bot, replies := newApprovalBot(agent)

	bot.handleMessage(context.Background(), adminApprovalEvent("bisa tolong jelaskan code?"))

	if len(agent.prompts) != 0 {
		t.Fatalf("unrelated prompt must not run during pending approval, got prompts: %v", agent.prompts)
	}
	if len(*replies) != 1 || !strings.Contains((*replies)[0], "menunggu persetujuan") {
		t.Fatalf("expected waiting for approval reminder, got: %v", *replies)
	}
}
