package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
)

// sendRecorder captures what the bridge sends to a chat.
type sendRecorder struct {
	mu    sync.Mutex
	chats []string
	texts []string
}

func (s *sendRecorder) send(ctx context.Context, chatID, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chats = append(s.chats, chatID)
	s.texts = append(s.texts, text)
	return nil
}

func (s *sendRecorder) snapshot() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.chats...), append([]string{}, s.texts...)
}

func newApprovalBridge(t *testing.T) (*whatsappBridge, *sendRecorder) {
	t.Helper()
	application := newTestApp(t)
	rec := &sendRecorder{}
	bridge := &whatsappBridge{app: application, name: "default", account: config.ChannelAccount{}, send: rec.send}
	return bridge, rec
}

func adminRequest(chat string) channel.Request {
	return channel.Request{Channel: config.ChannelWhatsApp, Account: "default", ChatID: chat, SenderID: "628111@s.whatsapp.net", Admin: true}
}

func TestWhatsAppApprovalRouteIsMissingForGuests(t *testing.T) {
	bridge, _ := newApprovalBridge(t)
	guest := channel.Request{ChatID: "628999@s.whatsapp.net", SenderID: "628999@s.whatsapp.net", Admin: false}
	if bridge.interactorFor(guest) != nil {
		t.Fatal("a guest turn must fail closed: no approval route, no answer")
	}
	if bridge.interactorFor(adminRequest("628111@s.whatsapp.net")) == nil {
		t.Fatal("an admin turn must carry an in-chat approval route")
	}
}

func TestWhatsAppApprovalRoundTrip(t *testing.T) {
	bridge, rec := newApprovalBridge(t)
	request := adminRequest("628111@s.whatsapp.net")
	approver, ok := bridge.interactorFor(request).(*whatsAppApprover)
	if !ok {
		t.Fatalf("the admin route must be the in-chat approver")
	}

	type outcome struct {
		decision agent.Decision
	}
	decided := make(chan outcome, 1)
	go func() {
		decided <- outcome{approver.Approve(agent.ApprovalRequest{Tool: "write_file", Risk: "edit", Detail: "Editing main.go"})}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bridge.mu.Lock()
		pending := bridge.pendingApproval != nil
		bridge.mu.Unlock()
		if pending {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	_, texts := rec.snapshot()
	if len(texts) == 0 {
		t.Fatal("the approval request must be posted in the chat")
	}
	prompt := texts[0]
	if !strings.Contains(prompt, "write_file") {
		t.Errorf("the prompt must name the tool, got %q", prompt)
	}
	if !strings.Contains(prompt, "Approval needed") && !strings.Contains(prompt, "Perlu persetujuan") {
		t.Errorf("the prompt must ask for the decision, got %q", prompt)
	}
	for _, key := range []string{"y", "s", "a", "n"} {
		if !strings.Contains(prompt, key) {
			t.Errorf("the prompt must advertise the answer key %q, got %q", key, prompt)
		}
	}

	if !bridge.ResolveApproval(request, "a") {
		t.Fatal("the operator's reply must answer the pending approval")
	}
	select {
	case got := <-decided:
		if got.decision != agent.DecisionAllowAlways {
			t.Errorf("decision = %v, want always", got.decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the answered approval must release the waiting turn")
	}
	_, confirmations := rec.snapshot()
	if len(confirmations) < 2 {
		t.Fatalf("the chat must confirm the decision, got %v", confirmations)
	}
	if !strings.Contains(confirmations[1], "Always allowed") && !strings.Contains(confirmations[1], "Selalu diizinkan") {
		t.Errorf("the confirmation must name the decision, got %q", confirmations[1])
	}
	if bridge.pendingApproval != nil {
		t.Error("the answered approval must not linger")
	}
}

func TestWhatsAppApprovalAnswerWords(t *testing.T) {
	cases := map[string]agent.Decision{
		"y":       agent.DecisionAllowOnce,
		"YA":      agent.DecisionAllowOnce,
		"iya":     agent.DecisionAllowOnce,
		"s":       agent.DecisionAllowSession,
		"sesi":    agent.DecisionAllowSession,
		"a":       agent.DecisionAllowAlways,
		"Selalu":  agent.DecisionAllowAlways,
		"n":       agent.DecisionDeny,
		"tidak":   agent.DecisionDeny,
		"bukan":   agent.DecisionDeny,
		"gimana?": agent.DecisionDeny,
		"":        agent.DecisionDeny,
	}
	for text, want := range cases {
		if got := mapApprovalAnswer(text); got != want {
			t.Errorf("mapApprovalAnswer(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestWhatsAppApprovalRejectsAnswersThatAreNotTheOperators(t *testing.T) {
	bridge, _ := newApprovalBridge(t)
	request := adminRequest("628111@s.whatsapp.net")
	answer := make(chan agent.Decision, 1)
	bridge.mu.Lock()
	bridge.pendingApproval = answer
	bridge.approvalChat = request.ChatID
	bridge.mu.Unlock()

	guest := request
	guest.Admin = false
	if bridge.ResolveApproval(guest, "y") {
		t.Error("a guest must not answer the operator's approval")
	}
	other := adminRequest("628999@s.whatsapp.net")
	if bridge.ResolveApproval(other, "y") {
		t.Error("another chat must not answer this chat's approval")
	}
	if bridge.ResolveApproval(request, "/status") {
		t.Error("a command must keep running instead of answering")
	}
	bridge.mu.Lock()
	still := bridge.pendingApproval
	chat := bridge.approvalChat
	bridge.mu.Unlock()
	if still == nil || chat != request.ChatID {
		t.Fatal("a refused answer must leave the approval waiting")
	}
}

func TestWhatsAppApprovalTimesOutClosed(t *testing.T) {
	bridge, rec := newApprovalBridge(t)
	previous := approvalTimeout
	approvalTimeout = 30 * time.Millisecond
	defer func() { approvalTimeout = previous }()

	approver := bridge.interactorFor(adminRequest("628111@s.whatsapp.net")).(*whatsAppApprover)
	if got := approver.Approve(agent.ApprovalRequest{Tool: "run_command", Risk: "command", Detail: "go test"}); got != agent.DecisionDeny {
		t.Errorf("an unanswered approval must deny, got %v", got)
	}
	_, texts := rec.snapshot()
	if len(texts) < 2 {
		t.Fatalf("the chat must be told the approval expired, got %v", texts)
	}
	if !strings.Contains(texts[1], "timed out") && !strings.Contains(texts[1], "habis waktu") {
		t.Errorf("the timeout note must say the approval expired, got %q", texts[1])
	}
}

// alwaysInteractor answers every approval with one fixed decision.
type alwaysInteractor struct {
	decision agent.Decision
}

func (i *alwaysInteractor) Approve(agent.ApprovalRequest) agent.Decision { return i.decision }
func (i *alwaysInteractor) Ask(string, []string) (string, error) {
	return "", errors.New("no questions in this test")
}

func TestChannelAlwaysPersistsTheToolGrant(t *testing.T) {
	application := newTestApp(t)
	application.SetInteractor(&alwaysInteractor{decision: agent.DecisionAllowAlways})

	if got := application.approve(agent.ApprovalRequest{Tool: "write_file", Risk: "edit", Detail: "Editing main.go"}); got != agent.DecisionAllowAlways {
		t.Fatalf("approve = %v, want always", got)
	}

	if !application.Policy().AlwaysAllowed["write_file"] {
		t.Error("the live policy must carry the always answer, or the next turn asks again")
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, name := range reloaded.AlwaysAllowedTools {
		if name == "write_file" {
			found = true
		}
	}
	if !found {
		t.Errorf("the always answer must persist in AlwaysAllowedTools, got %v", reloaded.AlwaysAllowedTools)
	}
	if !reloaded.AllowedInFolder(application.Workspace(), "write_file") {
		t.Error("the folder grant must persist beside the tool grant")
	}
}
