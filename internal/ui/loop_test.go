package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// send pushes one message through Update and returns the resulting model, the
// way the Bubble Tea runtime does.
func send(t *testing.T, model *Model, message tea.Msg) (*Model, tea.Cmd) {
	t.Helper()
	next, cmd := model.Update(message)
	updated, ok := next.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", next)
	}
	return updated, cmd
}

// TestTickKeepsTheClockOnlyWhileRunning pins that an idle terminal stops
// scheduling work: a tick that keeps rescheduling itself would burn a timer
// forever on a chat nobody is using.
func TestTickKeepsTheClockOnlyWhileRunning(t *testing.T) {
	model := chatModel(t)

	idle, cmd := send(t, model, tickMsg{})
	if cmd != nil {
		t.Errorf("an idle model should not reschedule the tick")
	}

	idle.running = true
	busy, cmd := send(t, idle, tickMsg{})
	if cmd == nil {
		t.Fatalf("a running model must keep ticking so the timer updates")
	}
	if !busy.running {
		t.Errorf("the tick must not clear the running flag")
	}
}

// TestSpinnerTickIsForwarded covers the animation frames. Bubble Tea delivers
// these whether or not anything is running, so they must not disturb the run.
func TestSpinnerTickIsForwarded(t *testing.T) {
	model := chatModel(t)
	// spinner.Tick returns the frame message itself, so this is exactly what
	// Bubble Tea delivers. A matching id returns the next frame command.
	frame, cmd := send(t, model, model.spin.Tick())
	if cmd == nil {
		t.Errorf("a spinner frame should schedule the next one")
	}
	if frame == nil {
		t.Fatalf("the model must survive a spinner frame")
	}
}

func TestRunDoneClearsTheFlagAndShowsTheError(t *testing.T) {
	model := chatModel(t)
	model.running = true

	done, _ := send(t, model, runDoneMsg{err: errors.New("provider exploded")})
	if done.running {
		t.Errorf("the run is over, so the running flag must be cleared")
	}
	if view := display(done); !strings.Contains(view, "provider exploded") {
		t.Errorf("the error should be visible in the transcript:\n%s", view)
	}

	// A clean finish adds nothing: the answer is already in the transcript.
	clean := chatModel(t)
	clean.running = true
	quiet, _ := send(t, clean, runDoneMsg{})
	if quiet.running {
		t.Errorf("the running flag must be cleared on a clean finish too")
	}
	for _, item := range quiet.blocks {
		if item.kind == blockError {
			t.Errorf("a clean finish must not add an error block: %q", item.text)
		}
	}
}

// TestApprovalRequestIsShownAndAnswered walks the whole approval handshake:
// the message installs the prompt, the key produces a decision, and the
// decision reaches the waiting agent goroutine.
func TestApprovalRequestIsShownAndAnswered(t *testing.T) {
	model := chatModel(t)
	reply := make(chan agent.Decision, 1)

	pending, _ := send(t, model, approvalRequestMsg{
		request: agent.ApprovalRequest{Tool: "write_file", Detail: "main.go"},
		reply:   reply,
	})
	if pending.pendingApproval == nil {
		t.Fatalf("the request should be pending after the message")
	}
	if !strings.Contains(display(pending), "write_file") {
		t.Errorf("the prompt should name the tool:\n%s", display(pending))
	}

	answered := press(t, pending, "y")
	if answered.pendingApproval != nil {
		t.Errorf("the prompt must be cleared once answered")
	}
	select {
	case decision := <-reply:
		if decision != agent.DecisionAllowOnce {
			t.Errorf("decision = %v, want allow once", decision)
		}
	default:
		t.Fatalf("no decision reached the waiting caller")
	}
}

func TestApprovalKeysMapToTheRightDecision(t *testing.T) {
	cases := []struct {
		key  string
		want agent.Decision
	}{
		{"y", agent.DecisionAllowOnce},
		{"enter", agent.DecisionAllowOnce},
		{"s", agent.DecisionAllowSession},
		{"a", agent.DecisionAllowAlways},
		{"n", agent.DecisionDeny},
		{"esc", agent.DecisionDeny},
	}
	for _, testCase := range cases {
		t.Run(testCase.key, func(t *testing.T) {
			model := chatModel(t)
			reply := make(chan agent.Decision, 1)
			pending, _ := send(t, model, approvalRequestMsg{
				request: agent.ApprovalRequest{Tool: "run_command"},
				reply:   reply,
			})
			press(t, pending, testCase.key)
			select {
			case decision := <-reply:
				if decision != testCase.want {
					t.Errorf("decision = %v, want %v", decision, testCase.want)
				}
			default:
				t.Fatalf("the key produced no decision")
			}
		})
	}
}

// TestApprovalIgnoresAnUnrelatedKey is the safety rule: a stray keystroke must
// not be read as consent, so the prompt stays up.
func TestApprovalIgnoresAnUnrelatedKey(t *testing.T) {
	model := chatModel(t)
	reply := make(chan agent.Decision, 1)
	pending, _ := send(t, model, approvalRequestMsg{
		request: agent.ApprovalRequest{Tool: "run_command"},
		reply:   reply,
	})

	stillPending := press(t, pending, "q")
	if stillPending.pendingApproval == nil {
		t.Fatalf("an unrelated key must leave the prompt up")
	}
	select {
	case decision := <-reply:
		t.Fatalf("an unrelated key produced a decision: %v", decision)
	default:
	}
}

func TestAlwaysAllowIsRemembered(t *testing.T) {
	model := chatModel(t)
	reply := make(chan agent.Decision, 1)
	pending, _ := send(t, model, approvalRequestMsg{
		request: agent.ApprovalRequest{Tool: "write_file"},
		reply:   reply,
	})
	press(t, pending, "a")
	<-reply

	// Two mechanisms, both needed: the live policy so this session stops
	// asking, and the saved list so the next launch stops asking too.
	if !pending.app.Policy().SessionAllowed["write_file"] {
		t.Errorf("'a' must silence the prompt for the rest of the session")
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
		t.Errorf("'a' must persist the tool, got %v", reloaded.AlwaysAllowedTools)
	}
}

func TestAskRequestPromptsAndAcceptsAnAnswer(t *testing.T) {
	model := chatModel(t)
	reply := make(chan string, 1)
	asking, cmd := send(t, model, askRequestMsg{
		question: "which port?",
		options:  []string{"3000", "8080"},
		reply:    reply,
	})
	if asking.pendingAsk == nil {
		t.Fatalf("the question should be pending after the message")
	}
	if cmd == nil {
		t.Errorf("the composer must be focused so the answer can be typed")
	}
	if asking.input.Placeholder != "Type an answer" {
		t.Errorf("placeholder = %q", asking.input.Placeholder)
	}
	if !strings.Contains(display(asking), "which port?") {
		t.Errorf("the question should be visible:\n%s", display(asking))
	}

	// An empty answer must not be sent: an accidental Enter would otherwise
	// answer the model's question with nothing.
	empty := press(t, asking, "enter")
	if empty.pendingAsk == nil {
		t.Fatalf("an empty answer must leave the question pending")
	}

	empty.input.SetValue("8080")
	answered := press(t, empty, "enter")
	if answered.pendingAsk != nil {
		t.Errorf("the question must be cleared once answered")
	}
	select {
	case answer := <-reply:
		if answer != "8080" {
			t.Errorf("answer = %q, want 8080", answer)
		}
	default:
		t.Fatalf("the answer never reached the waiting caller")
	}
	if !strings.Contains(display(answered), "8080") {
		t.Errorf("the answer should appear in the transcript:\n%s", display(answered))
	}
}

func TestAskRequestCanBeDismissed(t *testing.T) {
	model := chatModel(t)
	reply := make(chan string, 1)
	asking, _ := send(t, model, askRequestMsg{question: "which port?", reply: reply})

	dismissed := press(t, asking, "esc")
	if dismissed.pendingAsk != nil {
		t.Errorf("escape must clear the question")
	}
	select {
	case answer := <-reply:
		if answer != "(no answer)" {
			t.Errorf("answer = %q, want the explicit refusal", answer)
		}
	default:
		t.Fatalf("escape must still release the waiting caller, or the run hangs")
	}
}

// TestAskKeepsTypingBeforeEnter guards the composer: every other key has to
// reach the input field, or the answer could never be typed.
func TestAskKeepsTypingBeforeEnter(t *testing.T) {
	model := chatModel(t)
	reply := make(chan string, 1)
	asking, _ := send(t, model, askRequestMsg{question: "which port?", reply: reply})

	typed := press(t, asking, "8")
	if got := typed.input.Value(); got != "8" {
		t.Errorf("input = %q, want the typed rune", got)
	}
	if typed.pendingAsk == nil {
		t.Errorf("typing must not answer the question")
	}
}

func TestTelegramPairedNotice(t *testing.T) {
	model := chatModel(t)
	paired, _ := send(t, model, telegramPairedMsg{paired: true})
	if paired.notice != "Telegram paired." {
		t.Errorf("notice = %q", paired.notice)
	}

	// An unpaired message must not claim success.
	quiet := chatModel(t)
	unpaired, _ := send(t, quiet, telegramPairedMsg{paired: false})
	if unpaired.notice != "" {
		t.Errorf("notice = %q, want none", unpaired.notice)
	}
}

// TestEventMessageFoldsAndKeepsListening covers the event pump: the message
// folds the event, refreshes the view and returns the next read command.
func TestEventMessageFoldsAndKeepsListening(t *testing.T) {
	model := chatModel(t)
	folded, cmd := send(t, model, eventMsg(agent.Event{Kind: agent.EventText, Text: "hello"}))
	if cmd == nil {
		t.Errorf("the event pump must keep reading")
	}
	if !strings.Contains(display(folded), "hello") {
		t.Errorf("the event should be in the transcript:\n%s", display(folded))
	}
}

// unrelatedMsg stands in for anything the router does not claim, such as the
// paste, focus and mouse messages the terminal can send at any time.
type unrelatedMsg struct{}

// TestComposerHandlesOtherMessages proves the fallthrough: anything that is
// not one of the router's own messages belongs to the text area.
func TestComposerHandlesOtherMessages(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("draft text")

	after, _ := send(t, model, unrelatedMsg{})
	if after == nil {
		t.Fatalf("the model must survive an unrelated message")
	}
	if after.composer.Value() != "draft text" {
		t.Errorf("composer = %q, want the draft kept", after.composer.Value())
	}
}
