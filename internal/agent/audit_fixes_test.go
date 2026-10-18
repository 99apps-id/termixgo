package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

// TestSessionAnswerSilencesTheSameTurnGate pins the trust fix: a "session"
// answer at the untrusted-folder gate must silence later calls to the same
// tool in the same turn. The gate reads Env.SessionAllowed, so the runner has
// to record the answer there, not only on the policy.
func TestSessionAnswerSilencesTheSameTurnGate(t *testing.T) {
	approvals := 0
	approve := func(req ApprovalRequest) Decision {
		approvals++
		return DecisionAllowSession
	}
	runner, env, _ := newTestRunner(t, &fakeClient{}, &ApprovalPolicy{Mode: ApprovalAll}, approve)
	env.Trusted = false
	env.SessionAllowed = map[string]bool{}

	call := provider.ToolCall{ID: "c1", Name: "write_file", Arguments: `{"path":"a.txt","content":"one"}`}
	if result := runner.execute(context.Background(), call); result.IsError {
		t.Fatalf("first call: %v", result.Output)
	}
	call.ID = "c2"
	if result := runner.execute(context.Background(), call); result.IsError {
		t.Fatalf("second call: %v", result.Output)
	}
	if approvals != 1 {
		t.Errorf("approvals = %d, want 1: the session answer must cover the rest of the turn", approvals)
	}
}

// TestShortenStaysWithinItsMax pins the Shorten bound: the label plus its
// ellipsis must fit max, because the transcript reserves exactly that width.
func TestShortenStaysWithinItsMax(t *testing.T) {
	long := strings.Repeat("word ", 100)
	for _, max := range []int{10, 40, 240} {
		if got := Shorten(long, max); len(got) > max {
			t.Errorf("Shorten(%d) = %d bytes, want at most %d", max, len(got), max)
		}
	}
}

// TestHarnessPositiveDeltaApplies pins ApplyHarnessToBudget in both
// directions: a profile that grants a longer turn must actually lengthen it.
func TestHarnessPositiveDeltaApplies(t *testing.T) {
	if got := ApplyHarnessToBudget(10, HarnessProfile{StepBudgetDelta: 5}); got != 15 {
		t.Errorf("ApplyHarnessToBudget(10, +5) = %d, want 15", got)
	}
	if got := ApplyHarnessToBudget(10, HarnessProfile{StepBudgetDelta: -4}); got != 6 {
		t.Errorf("ApplyHarnessToBudget(10, -4) = %d, want 6", got)
	}
}

// TestSubagentRetryKeepsFirstPassSpend pins the read-only retry ledger: an
// empty first pass is retried with a mandate, and the retry must add to the
// first pass spend rather than replace it.
func TestSubagentRetryKeepsFirstPassSpend(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{usageChunk(100, 20), textChunk("Looks good.")},
		{usageChunk(50, 10), callChunk("c1", "read_file", `{"path":"note.txt"}`)},
		{usageChunk(10, 5), textChunk("Checked note.txt: fine.")},
	}}
	parent := testEnv(t)
	parent.Config = config.Default()
	if err := os.WriteFile(filepath.Join(parent.Workspace, "note.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, spend, err := RunSubagent(context.Background(), parent, client, provider.Model{ID: "test-model"}, string(SubagentCodeReview), "review note.txt", 4)
	if err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	// Both passes spent prompt tokens; the second pass alone would be 60.
	if spend.Usage.PromptTokens < 150 {
		t.Errorf("spend.Usage.PromptTokens = %d, want at least 150 from both passes", spend.Usage.PromptTokens)
	}
	// The child model itself is a delegated component, counted exactly once.
	if spend.Unpriced > 1 {
		t.Errorf("spend.Unpriced = %d, want at most 1: the child's own unpricedness lands once", spend.Unpriced)
	}
}
