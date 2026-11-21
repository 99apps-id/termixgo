package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

type planInteractor struct {
	review agent.PlanReview
	seen   string
}

func (p *planInteractor) Approve(agent.ApprovalRequest) agent.Decision { return agent.DecisionDeny }
func (p *planInteractor) Ask(string, []string) (string, error)         { return "", nil }
func (p *planInteractor) ReviewPlan(plan string) (agent.PlanReview, error) {
	p.seen = plan
	return p.review, nil
}

type askOnlyInteractor struct{ answer string }

func (a *askOnlyInteractor) Approve(agent.ApprovalRequest) agent.Decision { return agent.DecisionDeny }
func (a *askOnlyInteractor) Ask(string, []string) (string, error)         { return a.answer, nil }

func TestApprovedPlanSavesTheApprovalMode(t *testing.T) {
	application := newTestApp(t)
	_ = application.SetApprovalMode(config.ApprovalPlan)
	reviewer := &planInteractor{review: agent.PlanReview{Approved: true, Mode: agent.ApprovalEdits}}
	application.SetInteractor(reviewer)

	review, err := application.reviewPlan("the plan")
	if err != nil || !review.Approved || reviewer.seen != "the plan" {
		t.Fatalf("review = %+v, err = %v, seen %q", review, err, reviewer.seen)
	}
	if got := application.Config().ApprovalMode; got != config.ApprovalEdits {
		t.Fatalf("the next turn should be out of plan mode, mode = %s", got)
	}
}

func TestPlanReviewWithoutADialogAsks(t *testing.T) {
	application := newTestApp(t)
	_ = application.SetApprovalMode(config.ApprovalPlan)
	application.SetInteractor(&askOnlyInteractor{answer: "Approve and start"})
	review, err := application.reviewPlan("the plan")
	if err != nil || !review.Approved || review.Mode != agent.ApprovalAsk {
		t.Fatalf("review = %+v, err = %v", review, err)
	}
	if got := application.Config().ApprovalMode; got != config.ApprovalAsk {
		t.Fatalf("mode = %s", got)
	}
}
