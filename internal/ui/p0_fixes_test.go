package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// A lone Escape that is the head of a split mouse report must not deny an
// approval: the dialog holds it like the chat does, and the report body that
// follows drops it.
func TestApprovalEscapeFromAMouseReportDoesNotDeny(t *testing.T) {
	model := chatModel(t)
	reply := make(chan agent.Decision, 1)
	model.pendingApproval = &agent.ApprovalRequest{Tool: "run_command"}
	model.approvalReply = reply

	held := press(t, model, "esc")
	if !held.escPending || held.pendingApproval == nil {
		t.Fatalf("the escape should be held while the dialog stays open")
	}
	scrolled := press(t, held, "[<65;10;5M")
	if scrolled.pendingApproval == nil {
		t.Fatalf("a mouse report must not answer the approval")
	}
	select {
	case decision := <-reply:
		t.Fatalf("no decision should be sent, got %v", decision)
	default:
	}
}

// A real Escape still denies once the settle window ends.
func TestApprovalEscapeDeniesAfterSettle(t *testing.T) {
	model := chatModel(t)
	reply := make(chan agent.Decision, 1)
	model.pendingApproval = &agent.ApprovalRequest{Tool: "run_command"}
	model.approvalReply = reply

	settled := pressEsc(t, model)
	if settled.pendingApproval != nil {
		t.Fatalf("the approval should be closed")
	}
	if got := <-reply; got != agent.DecisionDeny {
		t.Fatalf("decision = %v, want deny", got)
	}
}

// !cmd runs off the event loop: the call returns a command at once and the
// output only lands when its message arrives.
func TestLocalCommandRunsOffTheEventLoop(t *testing.T) {
	model := chatModel(t)
	before := len(model.blocks)
	_, cmd := model.runLocalCommand("echo hi")
	if cmd == nil {
		t.Fatalf("the command should run as a tea.Cmd")
	}
	if len(model.blocks) != before+1 || model.blocks[len(model.blocks)-1].kind != blockUser {
		t.Fatalf("only the echo of the command should be shown before it finishes")
	}
	model.applyLocalCommand(localCommandDoneMsg{output: "hi"})
	last := model.blocks[len(model.blocks)-1]
	if last.kind != blockNotice || last.text != "hi" {
		t.Fatalf("the output should be appended, got %+v", last)
	}
	model.applyLocalCommand(localCommandDoneMsg{output: "boom", failed: true})
	if model.blocks[len(model.blocks)-1].kind != blockError {
		t.Fatalf("a failed command should render as an error")
	}
}

// The spinner stops ticking when no turn runs, so an idle screen does not
// repaint ten times a second.
func TestSpinnerDoesNotTickWhileIdle(t *testing.T) {
	model := chatModel(t)
	model.running = false
	_, cmd := model.Update(spinner.TickMsg{})
	if cmd != nil {
		t.Fatalf("an idle spinner must not schedule another tick")
	}
}

// Ctrl+T off collapses every thinking block to its header; on shows the body.
func TestShowReasoningControlsTheThinkingBody(t *testing.T) {
	model := chatModel(t)
	model.blocks = append(model.blocks, block{kind: blockThinking, reasoning: "secret chain of thought", finalized: true})

	_ = model.app.UpdateConfig(func(c *config.Config) { c.ShowReasoning = false })
	model.refresh()
	if strings.Contains(strings.Join(model.contentLines, "\n"), "secret chain of thought") {
		t.Fatalf("hidden reasoning should not be painted")
	}
	if model.blocks[len(model.blocks)-1].hideBody {
		t.Fatalf("hiding must not change the stored block")
	}

	_ = model.app.UpdateConfig(func(c *config.Config) { c.ShowReasoning = true })
	model.refresh()
	if !strings.Contains(stripANSI(strings.Join(model.contentLines, "\n")), "secret chain of thought") {
		t.Fatalf("visible reasoning should be painted")
	}
}
