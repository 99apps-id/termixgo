package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

// approvingParent builds a parent environment whose operator already answered
// "always" for write_file, and records every approval the nested run asks for.
func approvingParent(t *testing.T) (*Env, *[]string) {
	t.Helper()
	asked := &[]string{}
	parent := testEnv(t)
	cfg := config.Default()
	cfg.ApprovalMode = config.ApprovalAsk
	parent.Config = cfg
	parent.AlwaysAllowed = map[string]bool{"write_file": true}
	parent.Approve = func(request ApprovalRequest) Decision {
		*asked = append(*asked, request.Tool)
		return DecisionAllowOnce
	}
	return parent, asked
}

// TestSubagentInheritsAlwaysAllowed is the fix for an "allow always" answer
// that the nested run re-asked for on every call: the child built its policy
// from the approval mode alone, so the operator's answer reached the parent
// runner and nothing else.
func TestSubagentInheritsAlwaysAllowed(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "write_file", `{"path":"made.txt","content":"hello"}`)},
		{textChunk("Written.")},
	}}
	parent, asked := approvingParent(t)

	if _, _, err := RunSubagent(context.Background(), parent, client, provider.Model{ID: "test-model"}, string(SubagentBuilder), "create made.txt", 4); err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if len(*asked) != 0 {
		t.Errorf("the subagent asked for approval again for %v, want none: always-allowed must carry into the child", *asked)
	}
	if _, err := os.Stat(filepath.Join(parent.Workspace, "made.txt")); err != nil {
		t.Errorf("the write never happened: %v", err)
	}
}

// TestSubagentStillAsksForAToolTheOperatorDidNotAllow is the control: the
// inherited answer is per tool, not a blanket silence.
func TestSubagentStillAsksForAToolTheOperatorDidNotAllow(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "run_command", `{"command":"echo hi"}`)},
		{textChunk("Ran it.")},
	}}
	parent, asked := approvingParent(t)

	if _, _, err := RunSubagent(context.Background(), parent, client, provider.Model{ID: "test-model"}, string(SubagentBuilder), "run a command", 4); err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if len(*asked) == 0 {
		t.Errorf("a tool the operator never allowed must still ask in the child")
	}
}

// TestSubagentSessionAnswerSilencesTheFolderGate keeps the folder gate's own
// rule intact in the child: a "session" answer for this folder clears the tool,
// while an always-allowed tool in an untrusted folder still asks.
func TestSubagentSessionAnswerSilencesTheFolderGate(t *testing.T) {
	workspace := t.TempDir()
	policy := subagentPolicy(&Env{
		Workspace:      workspace,
		Config:         config.Default(),
		Trusted:        false,
		SessionAllowed: map[string]bool{"edit": true},
		AlwaysAllowed:  map[string]bool{"write_file": true},
	})
	child := &Env{
		Workspace:      workspace,
		Trusted:        false,
		SessionAllowed: map[string]bool{"edit": true},
		AlwaysAllowed:  map[string]bool{"write_file": true},
	}
	runner := &Runner{Env: child, Policy: policy, Tools: DefaultRegistry()}

	if runner.needsApprovalFor(&editTool{}) {
		t.Errorf("a session answer for this folder must clear the tool that follows it")
	}
	if !runner.needsApprovalFor(&writeFileTool{}) {
		t.Errorf("an always-allowed tool in an untrusted folder must still ask")
	}
}
