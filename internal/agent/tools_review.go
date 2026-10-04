package agent

import (
	"context"
	"strings"
)

// buildReviewPrompt wraps a diff for the reviewer. The pasted text is the
// starting point, not the whole job: a reviewer that answers from it alone
// has read none of the context the diff cannot show, so the prompt says to
// open files anyway. RunSubagent's read-tier judge backs the instruction
// with an outcome: zero tool calls twice discards the verdict.
func buildReviewPrompt(diff string, inspectHard bool) string {
	core := `Review the following git diff for correctness bugs, security risks and architecture issues. Report only ACTIONABLE findings, each as "[MUST/SHOULD/NIT] - issue -> fix". If nothing is wrong say "Looks good."`
	inspect := " The diff text alone hides context: open a changed file with read_file, and a caller or test where you need to judge behavior, before you verdict."
	if inspectHard {
		inspect = " The previous pass made no tool calls, so its verdict counted for nothing: before the verdict you MUST open the changed files with read_file, and at least one caller or test where the diff changes a contract."
	}
	return core + inspect + "\n\n```diff\n" + diff + "\n```"
}

// reviewChangesTool runs the read-only reviewer over the working change set
// before it is committed - the pre-commit gate the commit tools assume.
type reviewChangesTool struct{}

func (t *reviewChangesTool) Name() string      { return "review_changes" }
func (t *reviewChangesTool) Aliases() []string { return []string{"review_diff", "precommit_review"} }
func (t *reviewChangesTool) Mutating() bool    { return false }
func (t *reviewChangesTool) Risk() Risk        { return RiskEdit }
func (t *reviewChangesTool) Label(a map[string]any) string {
	return "Reviewing the change set"
}
func (t *reviewChangesTool) DoneLabel(a map[string]any) string {
	return "Reviewed the change set"
}
func (t *reviewChangesTool) Description() string {
	return "Run a read-only code-review subagent over the current git diff (or the diff since `base`, limited to `scope` or `staged`) and return its actionable findings. Use it after edits and before git_commit. A reviewer that opens no file is retried once; two empty passes come back as an error, not a false approval."
}
func (t *reviewChangesTool) Schema() map[string]any {
	return object(map[string]any{
		"staged": boolProp("Review the staged diff instead of the unstaged one."),
		"scope":  strProp("Limit the review to one path (e.g. internal/agent)."),
		"base":   strProp("Review the diff against this commit or branch instead of HEAD."),
	})
}

func (t *reviewChangesTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if env.RunSubagent == nil {
		return Result{Output: "Subagents are not available in this session, so the review cannot run.", IsError: true}, nil
	}
	var paths []string
	if scope := strings.TrimSpace(argString(args, "scope", "path")); scope != "" {
		paths = []string{scope}
	}
	argv, err := gitDiffArgv(argBool(args, "staged", false), false, strings.TrimSpace(argString(args, "base", "ref")), paths)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	diff, gitErr := runGit(ctx, env, argv...)
	if gitErr != nil {
		return Result{Output: diff, IsError: true}, nil
	}
	if strings.TrimSpace(diff) == "" {
		return Result{Output: "No changes to review."}, nil
	}
	// One attempt at the tool level; the empty-pass retry lives inside
	// RunSubagent where the tool-call count is visible.
	report, spend, runErr := env.RunSubagent(ctx, string(SubagentCodeReview), buildReviewPrompt(diff, false))
	if runErr != nil {
		return Result{
			Output:            runErr.Error(),
			IsError:           true,
			SubagentUsage:     spend.Usage,
			SubagentCost:      spend.Cost,
			SubagentCostKnown: spend.CostKnown,
			SubagentSpent:     true,
		}, nil
	}
	return Result{
		Output:            report,
		SubagentUsage:     spend.Usage,
		SubagentCost:      spend.Cost,
		SubagentCostKnown: spend.CostKnown,
		SubagentSpent:     true,
	}, nil
}
