package agent

import (
	"strings"
	"testing"
)

func TestReviewFinishPassesWhenPlanDoneAndVerified(t *testing.T) {
	ledger := NewVerifyLedger().RecordEdit("x.go").RecordVerification()
	v := ReviewFinish([]Todo{{ID: "t1", Title: "Done", Status: "completed"}}, ledger, false, 0)
	if v.BlockFinish {
		t.Fatalf("a finished plan with fresh evidence must pass the critic, got nudge %q", v.Nudge)
	}
}

func TestReviewFinishBlocksOnOpenPlan(t *testing.T) {
	ledger := NewVerifyLedger()
	v := ReviewFinish([]Todo{
		{ID: "t1", Title: "Read the parser", Status: "in_progress"},
		{ID: "t2", Title: "Write the fix", Status: "pending"},
	}, ledger, false, 0)
	if !v.BlockFinish {
		t.Fatal("an open plan must block the finish")
	}
	if !strings.Contains(v.Nudge, CriticNudgePrefix) || !strings.Contains(v.Nudge, "Read the parser") {
		t.Fatalf("the critic nudge must name the open work, got %q", v.Nudge)
	}
}

func TestReviewFinishBlocksOnUnverifiedEdit(t *testing.T) {
	ledger := NewVerifyLedger().RecordEdit("internal/agent/agent.go")
	v := ReviewFinish([]Todo{{ID: "t1", Title: "Done", Status: "completed"}}, ledger, false, 0)
	if !v.BlockFinish {
		t.Fatal("an unverified code edit must block the finish")
	}
}

func TestReviewFinishRespectsNudgeCap(t *testing.T) {
	ledger := NewVerifyLedger().RecordEdit("x.go")
	v := ReviewFinish([]Todo{{ID: "t1", Title: "Open", Status: "pending"}}, ledger, false, MaxCriticNudges)
	if v.BlockFinish {
		t.Fatal("the critic must stay silent once its nudge budget is spent")
	}
}

func TestReviewFinishWithHintsAttachesLearnedFixes(t *testing.T) {
	ledger := NewVerifyLedger().RecordEdit("x.go")
	hints := map[string][]string{"edit": {"read the file fresh before editing"}}
	v := ReviewFinishWithHints([]Todo{{ID: "t1", Title: "Done", Status: "completed"}}, ledger, false, 0, hints)
	if !v.BlockFinish {
		t.Fatal("an unverified edit must still block the finish")
	}
	if !strings.Contains(v.Nudge, "read the file fresh before editing") {
		t.Fatalf("the critic nudge must carry the learned fix, got %q", v.Nudge)
	}
}

func TestReviewFinishWithHintsStaysLeanWithoutHints(t *testing.T) {
	ledger := NewVerifyLedger().RecordEdit("x.go")
	v := ReviewFinishWithHints([]Todo{{ID: "t1", Title: "Done", Status: "completed"}}, ledger, false, 0, nil)
	if !v.BlockFinish {
		t.Fatal("an unverified edit must still block the finish")
	}
	if strings.Contains(v.Nudge, "Known fixes") {
		t.Fatalf("a hintless nudge must not carry a fixes section, got %q", v.Nudge)
	}
}

func TestNeedsPlanReminderFiresOnMutateWithoutPlan(t *testing.T) {
	if !NeedsPlanReminder(nil, true, 0) {
		t.Fatal("a mutation with no plan must trigger the reminder")
	}
	if !NeedsPlanReminder([]Todo{{ID: "t1", Title: "Done", Status: "completed"}}, true, 0) {
		t.Fatal("a mutation with only completed items must trigger the reminder")
	}
}

func TestNeedsPlanReminderStaysSilentWithActivePlan(t *testing.T) {
	todos := []Todo{{ID: "t1", Title: "Fix", Status: "in_progress"}}
	if NeedsPlanReminder(todos, true, 0) {
		t.Fatal("an active plan must silence the reminder")
	}
	pending := []Todo{{ID: "t1", Title: "Later", Status: "pending"}}
	if NeedsPlanReminder(pending, true, 0) {
		t.Fatal("a pending item must silence the reminder")
	}
	if NeedsPlanReminder(nil, false, 0) {
		t.Fatal("a non-mutating call must never trigger the reminder")
	}
	if NeedsPlanReminder(nil, true, MaxPlanNudges) {
		t.Fatal("the reminder must stay silent once its budget is spent")
	}
	if got := BuildPlanNudge(MaxPlanNudges); got != "" {
		t.Fatalf("a spent budget must render empty, got %q", got)
	}
}
