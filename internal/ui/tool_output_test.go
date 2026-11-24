package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

func TestCommandOutputPreviewInTheCompactView(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	output := "ok  pkg/a\nok  pkg/b\nFAIL pkg/c\n--- FAIL: TestX\nmore\nmore"
	item := block{kind: blockTool, toolLabel: "Ran go test ./...", toolOK: false, toolOutput: output, toolOutputLines: 120}

	compact := stripANSI(renderToolBlock(item, styles, 80, false))
	for _, want := range []string{"Ran go test ./...", "  ⎿  ok  pkg/a", "     FAIL pkg/c", "… +117 lines" + expandHint()} {
		if !strings.Contains(compact, want) {
			t.Fatalf("compact view lacks %q:\n%s", want, compact)
		}
	}
	if strings.Contains(compact, "TestX") {
		t.Fatalf("the compact view shows three lines only:\n%s", compact)
	}

	details := stripANSI(renderToolBlock(item, styles, 80, true))
	if !strings.Contains(details, "--- FAIL: TestX") || !strings.Contains(details, "… +114 lines") || strings.Contains(details, "ctrl+o") {
		t.Fatalf("details show the whole preview:\n%s", details)
	}
}

func TestToolEndCarriesThePreviewIntoTheBlock(t *testing.T) {
	model := chatModel(t)
	model.applyEvent(agent.Event{Kind: agent.EventToolStart, ToolName: "run_command", ToolLabel: "Running ls"})
	model.applyEvent(agent.Event{Kind: agent.EventToolEnd, ToolName: "run_command", ToolLabel: "Ran ls", ToolOK: true, ToolOutput: "a.go\nb.go", ToolOutputLines: 2})
	if view := display(model); !strings.Contains(view, "⎿  a.go") || !strings.Contains(view, "b.go") {
		t.Fatalf("the preview should be painted:\n%s", view)
	}
}
