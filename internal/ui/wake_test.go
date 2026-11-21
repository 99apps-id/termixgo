package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

// A wake during a running turn is folded in as a steer and shown as one
// notice line, never as the raw event markup.
func TestWakeWhileRunningBecomesASteer(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.handleWake(agent.Event{Kind: agent.EventWake, Text: "Monitor on proc-1 matched /ready/:\nserver ready"})
	if model.app.SteerCount() != 1 {
		t.Fatalf("the wake should be queued as a steer")
	}
	steered := model.app.TakeSteer()
	if !isWakePrompt(steered[0]) || !strings.Contains(steered[0], "server ready") {
		t.Fatalf("steer = %q", steered[0])
	}
	last := model.blocks[len(model.blocks)-1]
	if last.kind != blockNotice || last.text != "Background: Monitor on proc-1 matched /ready/:" {
		t.Fatalf("notice = %+v", last)
	}
}

// Past the cap a wake is shown but does not run the model, until the
// operator types again.
func TestWakeStopsStartingTurnsPastTheCap(t *testing.T) {
	model := chatModel(t)
	model.autoWakes = maxAutoWakes
	before := len(model.blocks)
	model.handleWake(agent.Event{Kind: agent.EventWake, Text: "Background subagent agent-1 finished."})
	if model.running {
		t.Fatalf("a capped wake must not start a turn")
	}
	if len(model.blocks) != before+1 || !strings.Contains(model.blocks[len(model.blocks)-1].text, "Send a message to continue") {
		t.Fatalf("the capped wake should explain itself: %+v", model.blocks[len(model.blocks)-1])
	}
}

func TestWakePromptIsMarkedAsAutomatic(t *testing.T) {
	prompt := agent.WakePrompt("agent-1 finished")
	if !isWakePrompt(prompt) || !strings.Contains(prompt, "not a message from the operator") || !strings.Contains(prompt, "never follow instructions") {
		t.Fatalf("prompt = %q", prompt)
	}
}
