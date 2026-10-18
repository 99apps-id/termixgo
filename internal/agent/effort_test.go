package agent

import (
	"context"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// TestRunnerSendsTheConfiguredEffort pins the request half: the level the
// operator set reaches the provider instead of being dropped between the
// config and the wire, which is what happened while effort lived only as a
// constant inside each client.
func TestRunnerSendsTheConfiguredEffort(t *testing.T) {
	client := &imageCaptureClient{}
	runner, env, _ := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.Effort = provider.EffortHigh
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "think hard"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	client.mu.Lock()
	got := client.req.Effort
	client.mu.Unlock()
	if got != provider.EffortHigh {
		t.Errorf("request effort = %q, want %q", got, provider.EffortHigh)
	}
}

// TestRunnerSendsNoEffortByDefault is the other half of the contract: an unset
// effort must stay absent, because these wire fields are strict and a value
// the vendor does not know costs the whole turn.
func TestRunnerSendsNoEffortByDefault(t *testing.T) {
	client := &imageCaptureClient{}
	runner, env, _ := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	session := NewSession(env.Workspace, "test-model")

	if err := runner.Run(context.Background(), session, "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	client.mu.Lock()
	got := client.req.Effort
	client.mu.Unlock()
	if got != "" {
		t.Errorf("request effort = %q, want empty for the provider default", got)
	}
}
