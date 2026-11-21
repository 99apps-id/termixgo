package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// PlanReview is the operator's verdict on a submitted plan.
type PlanReview struct {
	// Approved lets the work start.
	Approved bool
	// Mode is the approval mode to work in once approved ("edits" lets file
	// edits run, "ask" confirms each change). Empty keeps the current mode.
	Mode ApprovalMode
	// Feedback is what the operator wants changed when the plan is not
	// approved yet.
	Feedback string
}

// ErrPlanReviewUnsupported tells submit_plan the front end has no plan
// dialog, so it falls back to a plain question.
var ErrPlanReviewUnsupported = errors.New("plan review is not supported here")

// submitPlanTool is the plan-mode handshake: the model investigates without
// changing anything, then presents the plan, and the operator approves it (and
// picks how edits are confirmed) or sends it back with feedback. It is the
// equivalent of Claude Code's ExitPlanMode.
type submitPlanTool struct{}

func (t *submitPlanTool) Name() string { return "submit_plan" }
func (t *submitPlanTool) Aliases() []string {
	return []string{"exit_plan_mode", "present_plan", "propose_plan"}
}

// Not mutating: it changes nothing on disk, and it has to run in plan mode,
// where every mutating tool is blocked.
func (t *submitPlanTool) Mutating() bool              { return false }
func (t *submitPlanTool) Risk() Risk                  { return RiskEdit }
func (t *submitPlanTool) Label(map[string]any) string { return "Presenting the plan" }
func (t *submitPlanTool) DoneLabel(map[string]any) string {
	return "Presented the plan"
}
func (t *submitPlanTool) Description() string {
	return "Present a finished implementation plan to the operator and wait for approval before changing anything. Use it in plan mode once the investigation is done, and for any large or risky change. Write the plan in Markdown: the goal, the files to change and how, the order of steps, and how the result will be verified. If it comes back with feedback, revise and submit again; once approved, carry it out."
}
func (t *submitPlanTool) Schema() map[string]any {
	return object(map[string]any{
		"plan": strProp("The plan in Markdown: goal, files and changes, steps in order, verification."),
	}, "plan")
}

func (t *submitPlanTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	plan := strings.TrimSpace(argString(args, "plan", "content", "text"))
	if plan == "" {
		return Result{Output: "plan is required: write the plan in Markdown", IsError: true}, nil
	}
	if env == nil {
		return Result{Output: "no operator is available to review the plan", IsError: true}, nil
	}
	review, err := reviewPlan(env, plan)
	if err != nil {
		return Result{Output: "the plan could not be reviewed: " + err.Error(), IsError: true}, nil
	}
	if !review.Approved {
		feedback := strings.TrimSpace(review.Feedback)
		if feedback == "" {
			feedback = "none given"
		}
		return Result{
			Output:  fmt.Sprintf("The operator did not approve the plan yet. Feedback: %s\nStay in read-only investigation, revise the plan to address it, and call submit_plan again. Do not change anything until it is approved.", feedback),
			Display: "Plan sent back for changes",
		}, nil
	}
	text := "The operator approved the plan."
	if review.Mode != "" {
		text += fmt.Sprintf(" The approval mode is now %s, so the changes can be made.", review.Mode)
	}
	text += " Carry the plan out step by step, keep the todo list current, and verify the result as planned."
	return Result{Output: text, Display: "Plan approved", ApprovalMode: review.Mode}, nil
}

// reviewPlan asks through the plan dialog when the front end has one, else
// as a plain question with two answers.
func reviewPlan(env *Env, plan string) (PlanReview, error) {
	if env.ReviewPlan != nil {
		review, err := env.ReviewPlan(plan)
		if err == nil {
			return review, nil
		}
		if !errors.Is(err, ErrPlanReviewUnsupported) {
			return PlanReview{}, err
		}
	}
	if env.Ask == nil {
		return PlanReview{}, errors.New("no operator is available to review the plan")
	}
	return AskPlanReview(env.Ask, plan)
}

// AskPlanReview reviews a plan as a plain question with two answers, for a
// front end with no plan dialog (a chat channel, the plain mode). Anything
// other than the approve answer is taken as feedback.
func AskPlanReview(ask func(question string, options []string) (string, error), plan string) (PlanReview, error) {
	const approve, revise = "Approve and start", "Keep planning"
	answer, err := ask("Plan ready for approval:\n\n"+plan, []string{approve, revise})
	if err != nil {
		return PlanReview{}, err
	}
	trimmed := strings.TrimSpace(answer)
	lower := strings.ToLower(trimmed)
	if lower == strings.ToLower(approve) || lower == "1" || lower == "y" || lower == "yes" || strings.HasPrefix(lower, "approve") {
		return PlanReview{Approved: true, Mode: ApprovalAsk}, nil
	}
	if lower == strings.ToLower(revise) || lower == "2" {
		trimmed = ""
	}
	return PlanReview{Feedback: trimmed}, nil
}
