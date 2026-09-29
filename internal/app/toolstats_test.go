package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

// TestToolStatsCountsCallsAndFailures proves the /cost ledger: finished tool
// events accumulate per tool with failures flagged.
func TestToolStatsCountsCallsAndFailures(t *testing.T) {
	application := &App{}
	application.emit(agent.Event{Kind: agent.EventToolEnd, ToolName: "read_file", ToolOK: true})
	application.emit(agent.Event{Kind: agent.EventToolEnd, ToolName: "read_file", ToolOK: true})
	application.emit(agent.Event{Kind: agent.EventToolEnd, ToolName: "run_command", ToolOK: false})

	stats := application.ToolStats()
	if len(stats) != 2 {
		t.Fatalf("stats = %+v, want two tools", stats)
	}
	if stats[0].Name != "read_file" || stats[0].Calls != 2 {
		t.Errorf("first = %+v, want read_file with 2 calls", stats[0])
	}
	if stats[1].Name != "run_command" || stats[1].Errors != 1 {
		t.Errorf("second = %+v, want run_command with 1 error", stats[1])
	}
}

// TestToolStatsIgnoresUnnamedEvents keeps stray events out of the ledger.
func TestToolStatsIgnoresUnnamedEvents(t *testing.T) {
	application := &App{}
	application.emit(agent.Event{Kind: agent.EventToolEnd})
	application.emit(agent.Event{Kind: agent.EventText, Text: "hello"})
	if stats := application.ToolStats(); len(stats) != 0 {
		t.Errorf("stats = %+v, want empty", stats)
	}
}
