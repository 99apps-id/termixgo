package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// gateEnv builds a runner whose folder trust is explicit, so a test can pin
// what the trust gate does without the rest of the harness getting in the way.
//
// The policy is ApprovalAll on purpose: that is the mode that allows every
// tool, so a call that still waits can only be waiting because of trust.
func gateEnv(t *testing.T, trusted bool) (*Runner, *Env, *recorder) {
	t.Helper()
	runner, env, recorder := newTestRunner(t, &fakeClient{}, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	env.Trusted = trusted
	return runner, env, recorder
}

// TestTrustGateAsksForEveryMutatingTool is the rule behind the no-sandbox
// design: the allowlist is wide, so folder trust is the gate that keeps a
// mutating tool from running unattended in a folder nobody vouched for.
func TestTrustGateAsksForEveryMutatingTool(t *testing.T) {
	runner, _, _ := gateEnv(t, false)

	mutating := 0
	for _, tool := range DefaultRegistry().Tools() {
		if !tool.Mutating() {
			continue
		}
		mutating++
		if !runner.needsApprovalFor(tool) {
			t.Errorf("%s can change the machine, so it must ask in an untrusted folder", tool.Name())
		}
	}
	if mutating == 0 {
		t.Fatal("no mutating tool is registered, so this test proves nothing")
	}
}

// TestTrustGateLetsReadOnlyToolsThrough is the other half: a folder the
// operator has not trusted is still worth looking at, so reading must never
// stop for a prompt.
func TestTrustGateLetsReadOnlyToolsThrough(t *testing.T) {
	runner, _, _ := gateEnv(t, false)

	readOnly := 0
	for _, tool := range DefaultRegistry().Tools() {
		if tool.Mutating() {
			continue
		}
		readOnly++
		if runner.needsApprovalFor(tool) {
			t.Errorf("%s cannot change anything, so it must not ask", tool.Name())
		}
	}
	if readOnly == 0 {
		t.Fatal("no read-only tool is registered, so this test proves nothing")
	}
}

// TestTrustedFolderRunsTheAllowlist is the payoff: once the folder is trusted
// the agent stops asking, which is what makes it usable unattended.
func TestTrustedFolderRunsTheAllowlist(t *testing.T) {
	runner, _, _ := gateEnv(t, true)

	for _, tool := range DefaultRegistry().Tools() {
		if runner.needsApprovalFor(tool) {
			t.Errorf("%s must not ask in a trusted folder under approval all", tool.Name())
		}
	}
}

// TestUntrustedFolderAsksEvenWhenTheToolIsAlwaysAllowed pins the order of the
// two gates. A permanent allowance is a statement about a tool, not about a
// folder, so it must not be enough to run a write somewhere new.
func TestUntrustedFolderAsksEvenWhenTheToolIsAlwaysAllowed(t *testing.T) {
	policy := &ApprovalPolicy{
		Mode:           ApprovalAll,
		AlwaysAllowed:  map[string]bool{"write_file": true},
		SessionAllowed: map[string]bool{"edit": true},
	}
	runner, env, _ := newTestRunner(t, &fakeClient{}, policy, nil)
	env.Trusted = false

	for _, name := range []string{"write_file", "edit"} {
		tool, ok := DefaultRegistry().Lookup(name)
		if !ok {
			t.Fatalf("%s is missing from the registry", name)
		}
		if !runner.needsApprovalFor(tool) {
			t.Errorf("%s is always-allowed but the folder is untrusted, so it must still ask", name)
		}
	}
}

// TestUntrustedFolderDeniesWithoutAnOperator is the fail-safe: with nobody to
// answer, the gate refuses rather than writing first and asking later.
func TestUntrustedFolderDeniesWithoutAnOperator(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{
			callChunk("c1", "write_file", `{"path":"blocked.go","content":"package main"}`),
			callChunk("c2", "read_file", `{"path":"blocked.go"}`),
		},
		{textChunk("I could not write the file.")},
	}}
	runner, env, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	env.Trusted = false
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "write a file"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(env.Workspace, "blocked.go")); err == nil {
		t.Fatalf("an untrusted folder was written to without an answer")
	}
	// The denial has to reach the model, or it would keep retrying blind.
	if !conversationContains(session, "denied") {
		t.Errorf("the model was not told the call was denied")
	}
	// Reading still went through, so the refusal is about mutation and not
	// about the folder being closed for business.
	if conversationContains(session, "There is no tool named") {
		t.Errorf("a read-only tool was refused alongside the write")
	}
	if !recorder.has(EventToolEnd) {
		t.Errorf("the denied call should still be reported to the operator")
	}
}

// TestUntrustedFolderRunsAfterApproval covers the answer path: the operator's
// yes is what turns the gate off for that call.
func TestUntrustedFolderRunsAfterApproval(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "write_file", `{"path":"allowed.go","content":"package main"}`)},
		{textChunk("Written.")},
	}}
	var mu sync.Mutex
	var asked []string
	approve := func(request ApprovalRequest) Decision {
		mu.Lock()
		asked = append(asked, request.Tool)
		mu.Unlock()
		return DecisionAllowOnce
	}
	runner, env, _ := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, approve)
	env.Trusted = false
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "write a file"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 || asked[0] != "write_file" {
		t.Errorf("the operator was asked %q, want write_file once", asked)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace, "allowed.go")); err != nil {
		t.Errorf("an approved write should have run: %v", err)
	}
}

// TestSessionApprovalIsSafeWithoutAPolicy keeps the trust gate from turning a
// granted session into a crash: the gate can demand approval for a runner with
// no policy at all, and the grant still has to land somewhere.
func TestSessionApprovalIsSafeWithoutAPolicy(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "write_file", `{"path":"once.go","content":"package main"}`)},
		{textChunk("Written.")},
	}}
	approve := func(ApprovalRequest) Decision { return DecisionAllowSession }
	runner, env, _ := newTestRunner(t, client, nil, approve)
	env.Trusted = false
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "write a file"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace, "once.go")); err != nil {
		t.Errorf("an approved write should have run: %v", err)
	}
}

// TestApprovalRequestNamesTheRisk keeps the prompt useful: the operator has to
// see which tool is asking and how dangerous it is before answering.
func TestApprovalRequestNamesTheRisk(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "run_command", `{"command":"go test ./..."}`)},
		{textChunk("Done.")},
	}}
	var mu sync.Mutex
	var seen ApprovalRequest
	approve := func(request ApprovalRequest) Decision {
		mu.Lock()
		seen = request
		mu.Unlock()
		return DecisionDeny
	}
	runner, env, _ := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, approve)
	env.Trusted = false
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "run the tests"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if seen.Tool != "run_command" {
		t.Errorf("request.Tool = %q, want run_command", seen.Tool)
	}
	if seen.Risk != string(RiskCommand) {
		t.Errorf("request.Risk = %q, want %q", seen.Risk, RiskCommand)
	}
	if !strings.Contains(seen.Detail, "go test") {
		t.Errorf("request.Detail = %q, want the command named", seen.Detail)
	}
}
