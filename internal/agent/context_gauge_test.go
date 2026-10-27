package agent

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// TestContextHintReadsAgainstTheWindowNotTheBudget is the G-01 regression:
// a history that exactly fills the compaction budget of a large window is
// only half the window, and the ctx figure must say 50% while the compact
// figure separately says 100%. The old hint divided by the budget and
// reported 100% as if the window were full.
func TestContextHintReadsAgainstTheWindowNotTheBudget(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleUser, Content: strings.Repeat("x", 400000)}}
	used := EstimateMessages(messages)
	// HistoryBudget halves a window this size, so a window of twice the
	// history puts the history at exactly 100% of the budget.
	window := used * 2
	if budget := HistoryBudget(window); budget != used {
		t.Fatalf("test setup: HistoryBudget(%d) = %d, want %d", window, budget, used)
	}
	hint := ContextHint(messages, 0, window, "big-model")
	if !strings.Contains(hint, "(50% of window)") {
		t.Errorf("ctx figure should read 50%% of the window, got %q", hint)
	}
	if !strings.Contains(hint, "compact: 100%") {
		t.Errorf("compact figure should separately read 100%%, got %q", hint)
	}
	if !strings.Contains(hint, "big-model") {
		t.Errorf("the hint should name the model its denominator belongs to, got %q", hint)
	}
}

// TestContextHintCountsOverheadInTheWindowFigure checks the numerator: the
// system prompt and tool schemas ride in every request, so an empty history
// with a large overhead still occupies window. The overhead must not leak
// into the compact figure, which measures the stored history alone because
// that is what the compaction trim compares against the budget.
func TestContextHintCountsOverheadInTheWindowFigure(t *testing.T) {
	window := 100000
	hint := ContextHint(nil, window/4, window, "m")
	if !strings.Contains(hint, "(25% of window)") {
		t.Errorf("overhead should count toward the window figure, got %q", hint)
	}
	if !strings.Contains(hint, "compact: 0%") {
		t.Errorf("overhead should not count toward compaction proximity, got %q", hint)
	}
}

// TestEstimateToolSchemasCountsNamesDescriptionsAndSchemas pins the schema
// estimator: every part of a definition is sent to the provider.
func TestEstimateToolSchemasCountsNamesDescriptionsAndSchemas(t *testing.T) {
	defs := []provider.ToolDef{
		{
			Name:        "read_file",
			Description: strings.Repeat("d", 260),
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": strings.Repeat("s", 260)},
				},
			},
		},
	}
	got := EstimateToolSchemas(defs)
	if got <= 0 {
		t.Fatalf("schema estimate = %d, want a positive count", got)
	}
	// The same definition with an empty schema must estimate lower: the
	// schema JSON is part of the cost.
	bare := EstimateToolSchemas([]provider.ToolDef{{Name: "read_file", Description: strings.Repeat("d", 260)}})
	if got <= bare {
		t.Errorf("schema estimate %d should exceed the schema-less estimate %d", got, bare)
	}
	if empty := EstimateToolSchemas(nil); empty != 0 {
		t.Errorf("no definitions should estimate 0, got %d", empty)
	}
}
