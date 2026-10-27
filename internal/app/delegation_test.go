package app

import (
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestGuestDelegationNeedsOperatorApproval(t *testing.T) {
	application := newTestApp(t)
	parent := application.envForRun(config.FinalAgentName, true)

	// No interactor means the approval is denied, which fails closed.
	_, _, err := application.runSubagentFor(context.Background(), parent, config.FinalAgentName, true, "explore", "cek sesuatu")
	if err == nil || !strings.Contains(err.Error(), "did not approve") {
		t.Fatalf("a guest delegation without an operator must be refused, got %v", err)
	}

	// With an operator who allows, the gate passes and the run fails on the
	// missing provider client instead, which proves it got that far.
	application.SetInteractor(&fakeInteractor{})
	_, _, err = application.runSubagentFor(context.Background(), parent, config.FinalAgentName, true, "explore", "cek sesuatu")
	if err == nil || !strings.Contains(err.Error(), "no provider client") {
		t.Fatalf("an approved delegation must reach the runner, got %v", err)
	}
}

func TestDelegationPerTurnCap(t *testing.T) {
	application := newTestApp(t)
	application.mu.Lock()
	application.turnDelegations = config.DefaultDelegationChildren
	application.mu.Unlock()

	_, _, err := application.runSubagentFor(context.Background(), application.envForRun(config.FinalAgentName, false), config.FinalAgentName, false, "explore", "satu lagi")
	if err == nil || !strings.Contains(err.Error(), "delegation limit") {
		t.Fatalf("the per-turn fan-out cap must stop the call, got %v", err)
	}
}

func TestAutoApproveFalseAsksTheOperator(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	no := false
	cfg := config.Default()
	cfg = cfg.WithAgent("sales", config.Agent{Delegation: config.Delegation{AutoApprove: &no}})
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, _, err = application.runSubagentFor(context.Background(), application.envForRun("sales", false), "sales", false, "explore", "cek")
	if err == nil || !strings.Contains(err.Error(), "did not approve") {
		t.Fatalf("autoApprove=false must ask first, got %v", err)
	}
}

func TestEnvCarriesDelegationLimitsAndDenyList(t *testing.T) {
	application := newTestApp(t)
	env := application.envForRun(config.FinalAgentName, true)
	if env.DelegationMaxChildren != config.DefaultDelegationChildren {
		t.Errorf("DelegationMaxChildren = %d", env.DelegationMaxChildren)
	}
	found := false
	for _, name := range env.ToolDeny {
		if name == "run_command" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("the guest deny-list must reach the subagent environment: %v", env.ToolDeny)
	}
}
