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

// TestPlanModeNeedsApprovalForWrites pins the policy half: every mutating
// tool waits, reads never do.
func TestPlanModeNeedsApprovalForWrites(t *testing.T) {
	policy := ApprovalPolicy{Mode: ApprovalPlan}
	for _, name := range []string{"edit", "write_file", "run_command", "git_commit"} {
		tool, ok := DefaultRegistry().Lookup(name)
		if !ok {
			t.Fatalf("missing tool %q", name)
		}
		if !policy.NeedsApproval(tool) {
			t.Errorf("plan mode should gate %q", name)
		}
	}
	read, _ := DefaultRegistry().Lookup("read_file")
	if policy.NeedsApproval(read) {
		t.Errorf("plan mode must not gate reads")
	}
}

// TestPlanModeBlocksWithoutAsking proves the runner half: a mutating call is
// denied with guidance and the operator is never prompted.
func TestPlanModeBlocksWithoutAsking(t *testing.T) {
	runner, env, _ := newTestRunner(t, nil, &ApprovalPolicy{Mode: ApprovalPlan}, func(ApprovalRequest) Decision {
		t.Errorf("plan mode must not ask the operator")
		return DecisionAllowOnce
	})
	if err := os.WriteFile(filepath.Join(env.Workspace, "note.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	denied := runner.execute(context.Background(), provider.ToolCall{Name: "edit", Arguments: `{"path":"note.txt","old_string":"hello","new_string":"bye"}`})
	if !denied.IsError {
		t.Errorf("a mutating call in plan mode should fail, got %q", denied.Output)
	}
	if !strings.Contains(denied.Output, "Plan mode") {
		t.Errorf("the denial should name the way back, got %q", denied.Output)
	}

	allowed := runner.execute(context.Background(), provider.ToolCall{Name: "read_file", Arguments: `{"path":"note.txt"}`})
	if allowed.IsError {
		t.Errorf("a read in plan mode should run, got %q", allowed.Output)
	}
}

// TestPlanModePromptWarnsTheModel keeps the model from retrying blocked
// calls: the system prompt has to state the mode.
func TestPlanModePromptWarnsTheModel(t *testing.T) {
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Config: config.Default(), Todos: NewTodoStore(), Memory: NewMemory(workspace)}
	env.Config.ApprovalMode = config.ApprovalPlan
	if system := BuildSystem(env, "test"); !strings.Contains(system, "Plan mode is ON") {
		t.Errorf("the prompt should state plan mode:\n%s", system)
	}

	env.Config.ApprovalMode = config.ApprovalAll
	if system := BuildSystem(env, "test"); strings.Contains(system, "Plan mode is ON") {
		t.Errorf("the prompt should not mention plan mode outside it:\n%s", system)
	}
}

// TestPlanModeApprovalRoundTrip covers the config bridge: "plan" parses and
// resolves instead of falling back to all.
func TestPlanModeApprovalRoundTrip(t *testing.T) {
	mode, err := config.ParseApprovalMode("plan")
	if err != nil {
		t.Fatalf("ParseApprovalMode(plan): %v", err)
	}
	cfg := config.Default()
	cfg.ApprovalMode = mode
	if got := ApprovalModeOrDefault(cfg); got != ApprovalPlan {
		t.Errorf("ApprovalModeOrDefault = %q, want plan", got)
	}
	if mode, err := config.ParseApprovalMode("pl"); err != nil || mode != config.ApprovalPlan {
		t.Errorf("a unique prefix should resolve to plan, got %q, %v", mode, err)
	}
}
