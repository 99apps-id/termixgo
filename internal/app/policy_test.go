package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// TestPolicyKeepsItsMemoryAcrossAConfigChange pins the field a policy rewrite
// used to drop.
//
// UpdateConfig and SetApprovalMode build a new ApprovalPolicy to change the
// mode. Replacing it without Memory left the runner unable to record "the
// operator allowed this tool" into learned memory, and because the runner
// nil-checks the field the defect was silent: approval decisions simply stopped
// being learned after the first config write of the session.
func TestPolicyKeepsItsMemoryAcrossAConfigChange(t *testing.T) {
	application := newTestApp(t)
	if application.Policy().Memory == nil {
		t.Fatalf("a fresh app should carry the memory the runner records into")
	}

	application.Policy().AllowSession("run_command")
	if err := application.UpdateConfig(func(cfg *config.Config) { cfg.HarnessProfile = "autonomous" }); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	policy := application.Policy()
	if policy.Memory == nil {
		t.Errorf("UpdateConfig dropped the policy memory, so approval decisions stop being learned")
	}
	if !policy.SessionAllowed["run_command"] {
		t.Errorf("UpdateConfig dropped the session allowances")
	}

	if err := application.SetApprovalMode(config.ApprovalPlan); err != nil {
		t.Fatalf("SetApprovalMode: %v", err)
	}
	policy = application.Policy()
	if policy.Memory == nil {
		t.Errorf("SetApprovalMode dropped the policy memory")
	}
	if !policy.SessionAllowed["run_command"] {
		t.Errorf("SetApprovalMode dropped the session allowances")
	}
	if policy.Mode != agent.ApprovalPlan {
		t.Errorf("mode = %q, want plan", policy.Mode)
	}
}
