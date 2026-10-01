package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/99apps-id/termixgo/internal/skill"
)

// TestProposeSkillStagesWithoutWritingLive pins the workshop contract: the tool
// records a proposal but never touches the live skill, so generated guidance
// cannot rewrite itself without the operator.
func TestProposeSkillStagesWithoutWritingLive(t *testing.T) {
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true}
	tool := &proposeSkillTool{}

	result, err := tool.Run(context.Background(), env, map[string]any{
		"name":        "demo",
		"description": "A demo skill.",
		"body":        "# Demo\n\nDo the thing.",
		"summary":     "because it repeats",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("propose is an error: %q", result.Output)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".termixgo", "skills", "demo", "SKILL.md")); !os.IsNotExist(err) {
		t.Errorf("the live skill must not be written by a proposal")
	}
	proposals, err := skill.ListProposals(workspace)
	if err != nil || len(proposals) != 1 {
		t.Fatalf("ListProposals = %v, %v", proposals, err)
	}
	if proposals[0].Action != skill.ProposalCreate {
		t.Errorf("action = %q, want create", proposals[0].Action)
	}
}
