package agent

import "testing"

// A scheduled prompt makes the agent act later without anyone watching, so
// plan mode blocks it (the runner blocks every mutating tool) and the approval
// policy asks about it, in edits mode too.
func TestScheduleTaskIsGatedLikeAMutation(t *testing.T) {
	tool := &scheduleTaskTool{}
	if !tool.Mutating() {
		t.Fatalf("schedule_task must be mutating so plan mode and approvals apply")
	}
	for mode, want := range map[ApprovalMode]bool{ApprovalAsk: true, ApprovalEdits: true, ApprovalAll: false} {
		policy := ApprovalPolicy{Mode: mode}
		if got := policy.NeedsApproval(tool); got != want {
			t.Fatalf("mode %s: NeedsApproval = %v, want %v", mode, got, want)
		}
	}
}
