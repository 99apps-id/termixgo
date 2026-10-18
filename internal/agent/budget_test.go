package agent

import (
	"context"
	"strings"
	"testing"
)

func TestHistoryBudgetReservesRoomAndKeepsAFloor(t *testing.T) {
	// A large window leaves half of itself to the conversation: the reserve
	// grows with the window so a huge context cannot crowd out the prompt, the
	// tool schemas and the model's own answer.
	large := HistoryBudget(200000)
	if want := 100000; large != want {
		t.Errorf("large window budget = %d, want %d", large, want)
	}

	// A small local window would go negative after the reserve, so the floor
	// is what stops every earlier turn being discarded.
	small := HistoryBudget(32768)
	if small <= 0 {
		t.Fatalf("small window budget = %d, want a positive value", small)
	}
	if floor := 32768 * 30 / 100; small < floor {
		t.Errorf("small window budget = %d, want at least the floor %d", small, floor)
	}
	if small >= 32768 {
		t.Errorf("small window budget = %d, want less than the whole window", small)
	}

	// An unknown window must still produce the conservative default.
	if got := HistoryBudget(0); got != defaultContextBudget {
		t.Errorf("unknown window budget = %d, want %d", got, defaultContextBudget)
	}
	if got := HistoryBudget(-5); got != defaultContextBudget {
		t.Errorf("negative window budget = %d, want %d", got, defaultContextBudget)
	}
}

func TestSubagentDepthCapIsReachable(t *testing.T) {
	// The subagent tool refuses to nest beyond the cap, so the value the
	// runner assigns has to be able to reach it for the cap to mean anything.
	grandchild := &Env{Depth: MaxSubagentDepth, Todos: NewTodoStore()}
	// A grandchild does have a runner injected, so the depth cap, not the
	// availability check, is what has to stop it.
	grandchild.RunSubagent = func(context.Context, string, string) (string, SubagentSpend, error) { return "", SubagentSpend{}, nil }
	child := &Env{Depth: 1, Todos: NewTodoStore()}

	tool := &subagentTool{}

	refused, err := tool.Run(context.Background(), grandchild, map[string]any{"prompt": "dig deeper"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !refused.IsError || !strings.Contains(refused.Output, "cannot nest") {
		t.Errorf("depth 3 must be refused with an explanation, got %+v", refused)
	}

	// A child without a runner reports that delegation is unavailable, which
	// is a different refusal from the depth cap.
	unavailable, err := tool.Run(context.Background(), child, map[string]any{"prompt": "dig"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !unavailable.IsError || !strings.Contains(unavailable.Output, "not available") {
		t.Errorf("a child without a runner should report unavailability, got %+v", unavailable)
	}
}
