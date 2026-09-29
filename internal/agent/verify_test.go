package agent

import (
	"testing"
)

// TestVerifyLedgerNudgesAfterACodeEdit is the verify-on-stop contract: a
// code edit with no fresh passing check demands verification, and a passing
// check clears the debt.
func TestVerifyLedgerNudgesAfterACodeEdit(t *testing.T) {
	ledger := NewVerifyLedger()
	if ledger.ShouldNudgeVerification(false) {
		t.Fatalf("an empty ledger must stay silent")
	}

	ledger = ledger.RecordEdit("main.go")
	if !ledger.ShouldNudgeVerification(false) {
		t.Errorf("an unverified code edit should nudge")
	}
	if nudge := ledger.BuildVerifyNudge(0, false); nudge == "" {
		t.Errorf("the nudge should name the work")
	}

	ledger = ledger.RecordVerification()
	if ledger.ShouldNudgeVerification(false) {
		t.Errorf("fresh passing evidence should clear the nudge")
	}
}

// TestVerifyLedgerIgnoresProseEdits keeps docs cheap: editing only prose
// never demands a test run.
func TestVerifyLedgerIgnoresProseEdits(t *testing.T) {
	ledger := NewVerifyLedger().RecordEdit("README.md")
	if ledger.ShouldNudgeVerification(false) {
		t.Errorf("a prose edit alone must not nudge")
	}
}

// TestVerifyLedgerNudgesOnClaimWithoutEvidence closes the claimed-but-never
// ran hole: a finale that says tests pass with no evidence gets one nudge.
func TestVerifyLedgerNudgesOnClaimWithoutEvidence(t *testing.T) {
	ledger := NewVerifyLedger()
	if !ledger.ShouldNudgeVerification(true) {
		t.Errorf("a verification claim with no evidence should nudge")
	}
	if nudge := ledger.BuildVerifyNudge(0, true); nudge == "" {
		t.Errorf("the claim nudge should explain what is missing")
	}
}

// TestVerifyNudgeBudgetBoundsFollowUps stops the gate from nagging forever:
// after two nudges it stays silent.
func TestVerifyNudgeBudgetBoundsFollowUps(t *testing.T) {
	ledger := NewVerifyLedger().RecordEdit("main.go")
	if ledger.BuildVerifyNudge(MaxVerifyNudges, false) != "" {
		t.Errorf("an exhausted nudge budget must stay silent")
	}
}

// TestLooksLikeCheckCommandSeparatesChecksFromNoise keeps ls from counting
// as verification while go test does.
func TestLooksLikeCheckCommandSeparatesChecksFromNoise(t *testing.T) {
	if !LooksLikeCheckCommand("go test ./...") {
		t.Errorf("go test should count as verification")
	}
	if LooksLikeCheckCommand("ls -la") {
		t.Errorf("ls must not count as verification")
	}
}
