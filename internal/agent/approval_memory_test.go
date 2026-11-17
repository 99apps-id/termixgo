package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// approvalMemoryRunner builds a runner whose approval answers are fixed, with
// learned memory pointed at a throwaway workspace, so a test can pin exactly
// which operator answers end up as durable facts.
func approvalMemoryRunner(t *testing.T, decision Decision) (*Runner, string) {
	t.Helper()
	workspace := t.TempDir()
	memory := NewMemory(workspace)
	runner, _, _ := newTestRunner(t, &fakeClient{}, &ApprovalPolicy{
		Mode:   ApprovalAsk,
		Memory: memory,
	}, func(ApprovalRequest) Decision { return decision })
	return runner, workspace
}

func approvalMemoryFacts(t *testing.T, workspace string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workspace, ".termixgo", "memory.md"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read memory file: %v", err)
	}
	return string(data)
}

func writeFileCall() provider.ToolCall {
	return provider.ToolCall{
		ID:        "c1",
		Name:      "write_file",
		Arguments: `{"path":"note.txt","content":"hi"}`,
	}
}

// A one-time allowance is scoped to this turn: it must not become a durable
// fact that later sessions inherit.
func TestAllowOnceIsNotRecordedInMemory(t *testing.T) {
	runner, workspace := approvalMemoryRunner(t, DecisionAllowOnce)
	if result := runner.execute(context.Background(), writeFileCall()); result.IsError {
		t.Fatalf("execute: %v", result.Output)
	}
	if facts := approvalMemoryFacts(t, workspace); strings.Contains(facts, "write_file") {
		t.Errorf("allow-once must not be remembered, got:\n%s", facts)
	}
}

// A session allowance dies with the session for the same reason.
func TestAllowSessionIsNotRecordedInMemory(t *testing.T) {
	runner, workspace := approvalMemoryRunner(t, DecisionAllowSession)
	if result := runner.execute(context.Background(), writeFileCall()); result.IsError {
		t.Fatalf("execute: %v", result.Output)
	}
	if facts := approvalMemoryFacts(t, workspace); strings.Contains(facts, "write_file") {
		t.Errorf("allow-session must not be remembered, got:\n%s", facts)
	}
}

// Only a permanent allowance is durable by definition, so only it is recorded.
func TestAllowAlwaysIsRecordedInMemory(t *testing.T) {
	runner, workspace := approvalMemoryRunner(t, DecisionAllowAlways)
	if result := runner.execute(context.Background(), writeFileCall()); result.IsError {
		t.Fatalf("execute: %v", result.Output)
	}
	facts := approvalMemoryFacts(t, workspace)
	if !strings.Contains(facts, "approval always_allowed write_file") {
		t.Errorf("allow-always must be remembered, got:\n%s", facts)
	}
}

// A denial leaves no trace either: there is no allowance to remember.
func TestDenyIsNotRecordedInMemory(t *testing.T) {
	runner, workspace := approvalMemoryRunner(t, DecisionDeny)
	if result := runner.execute(context.Background(), writeFileCall()); !result.IsError {
		t.Fatalf("a denied call must fail, got %q", result.Output)
	}
	if facts := approvalMemoryFacts(t, workspace); strings.Contains(facts, "write_file") {
		t.Errorf("deny must not be remembered, got:\n%s", facts)
	}
}
