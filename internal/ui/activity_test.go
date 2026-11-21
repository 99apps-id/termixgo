package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
)

func TestActivityLineFollowsTheTurn(t *testing.T) {
	model := inlineModel(t)
	if model.activityLine() != "" {
		t.Fatalf("no activity line while idle")
	}
	model.blocks = append(model.blocks, block{kind: blockUser, text: "fix it"})
	model.running = true
	model.runStarted = time.Now().Add(-14 * time.Second)
	model.beginTurnStats()

	model.applyEvent(agent.Event{Kind: agent.EventThinking, Text: strings.Repeat("x", 8400)})
	line := stripANSI(model.activityLine())
	for _, want := range []string{"Thinking…", "14s", "↓ 2.1k tokens", "esc to interrupt"} {
		if !strings.Contains(line, want) {
			t.Fatalf("activity line %q lacks %q", line, want)
		}
	}
	if view := stripANSI(model.View()); strings.Contains(view, "Thinking...") {
		t.Fatalf("the live transcript must not repeat the thinking header:\n%s", view)
	}

	model.applyEvent(agent.Event{Kind: agent.EventToolStart, ToolName: "run_command", ToolLabel: "Running go test ./..."})
	if line := stripANSI(model.activityLine()); !strings.Contains(line, "Running go test ./…") {
		t.Fatalf("a running tool names itself: %q", line)
	}
	model.applyEvent(agent.Event{Kind: agent.EventToolEnd, ToolName: "run_command", ToolOK: true})
	model.applyEvent(agent.Event{Kind: agent.EventText, Text: "Done."})
	if line := stripANSI(model.activityLine()); !strings.Contains(line, "Writing…") {
		t.Fatalf("streaming text reads as writing: %q", line)
	}
	if hints := stripANSI(model.viewHints()); strings.Contains(hints, "working (") {
		t.Fatalf("the inline hint row must not repeat the working indicator: %q", hints)
	}
}
