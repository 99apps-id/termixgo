package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// githubCommandTimeout bounds one gh invocation.
const githubCommandTimeout = 120 * time.Second

// maxGitHubOutputChars caps what a gh command may return.
const maxGitHubOutputChars = 24000

// runGH executes the GitHub CLI with explicit arguments, never through a shell.
//
// The arguments carry a PR title, a body and a branch, any of which may contain
// a shell metacharacter, so they are passed as separate argv entries exactly the
// way git is. gh is a normal command a developer already trusts; this only keeps
// a model-authored string literal.
func runGH(ctx context.Context, env *Env, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, githubCommandTimeout)
	defer cancel()

	command := exec.CommandContext(runCtx, "gh", args...)
	command.Dir = env.Workspace
	var buffer bytes.Buffer
	command.Stdout = &buffer
	command.Stderr = &buffer
	command.Stdin = strings.NewReader("")

	err := command.Run()
	output := strings.TrimRight(buffer.String(), "\n")
	if len(output) > maxGitHubOutputChars {
		output = clipBytes(output, maxGitHubOutputChars) + "\n... [output truncated]"
	}
	if runCtx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("gh %s timed out after %s", strings.Join(args, " "), githubCommandTimeout)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if strings.TrimSpace(output) == "" {
				output = fmt.Sprintf("gh exited with code %d", exitErr.ExitCode())
			}
			return output, fmt.Errorf("gh %s failed: %s", githubCommandName(args), output)
		}
		return output, fmt.Errorf("could not run gh: %w. Install the GitHub CLI and run 'gh auth login'", err)
	}
	return output, nil
}

func githubCommandName(args []string) string {
	if len(args) == 0 {
		return "command"
	}
	if len(args) >= 2 {
		return args[0] + " " + args[1]
	}
	return args[0]
}

// githubResult wraps a gh failure as a tool result rather than a Go error.
func githubResult(ctx context.Context, env *Env, args ...string) Result {
	output, err := runGH(ctx, env, args...)
	if err != nil {
		if strings.TrimSpace(output) != "" {
			return Result{Output: output, IsError: true}
		}
		return Result{Output: err.Error(), IsError: true}
	}
	if strings.TrimSpace(output) == "" {
		output = "gh returned no output."
	}
	return Result{Output: output}
}

const githubPRFields = "number,title,state,url,author,headRefName,baseRefName"

type githubCreatePRTool struct{}

func (t *githubCreatePRTool) Name() string      { return "github_create_pr" }
func (t *githubCreatePRTool) Aliases() []string { return []string{"create_pr", "gh_create_pr"} }
func (t *githubCreatePRTool) Mutating() bool    { return true }
func (t *githubCreatePRTool) Risk() Risk        { return RiskCommand }
func (t *githubCreatePRTool) Label(a map[string]any) string {
	return "Opening PR " + Shorten(argString(a, "title"), 40)
}
func (t *githubCreatePRTool) DoneLabel(a map[string]any) string {
	return "Opened PR " + Shorten(argString(a, "title"), 40)
}
func (t *githubCreatePRTool) Description() string {
	return "Create a GitHub pull request from the current branch with the gh CLI. Use after git_commit to publish the work. Needs gh installed and authenticated."
}
func (t *githubCreatePRTool) Schema() map[string]any {
	return object(map[string]any{
		"title": strProp("PR title."),
		"body":  strProp("PR body, Markdown. Optional."),
		"base":  strProp("Target branch. Defaults to the repository default."),
	}, "title")
}

func (t *githubCreatePRTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	title := strings.TrimSpace(argString(args, "title"))
	if title == "" {
		return Result{Output: "title is required", IsError: true}, nil
	}
	argv := []string{"pr", "create", "--title", title}
	if body := argString(args, "body"); strings.TrimSpace(body) != "" {
		argv = append(argv, "--body", body)
	} else {
		argv = append(argv, "--body", "")
	}
	if base := strings.TrimSpace(argString(args, "base")); base != "" {
		argv = append(argv, "--base", base)
	}
	return githubResult(ctx, env, argv...), nil
}

type githubGetPRTool struct{}

func (t *githubGetPRTool) Name() string      { return "github_get_pr" }
func (t *githubGetPRTool) Aliases() []string { return []string{"get_pr", "gh_pr_view"} }
func (t *githubGetPRTool) Mutating() bool    { return false }
func (t *githubGetPRTool) Risk() Risk        { return RiskNetwork }
func (t *githubGetPRTool) Label(a map[string]any) string {
	return fmt.Sprintf("Reading PR #%d", argInt(a, "number", 0, 0, 0))
}
func (t *githubGetPRTool) DoneLabel(a map[string]any) string {
	return fmt.Sprintf("Read PR #%d", argInt(a, "number", 0, 0, 0))
}
func (t *githubGetPRTool) Description() string {
	return "Read a pull request's details, comments and reviews with the gh CLI. Read-only."
}
func (t *githubGetPRTool) Schema() map[string]any {
	return object(map[string]any{"number": intProp("Pull request number.")}, "number")
}

func (t *githubGetPRTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	number := argInt(args, "number", 0, 0, 0)
	if number <= 0 {
		return Result{Output: "number is required", IsError: true}, nil
	}
	return githubResult(ctx, env, "pr", "view", fmt.Sprintf("%d", number),
		"--json", githubPRFields+",body,comments,reviews"), nil
}

type githubListPRsTool struct{}

func (t *githubListPRsTool) Name() string      { return "github_list_prs" }
func (t *githubListPRsTool) Aliases() []string { return []string{"list_prs", "gh_pr_list"} }
func (t *githubListPRsTool) Mutating() bool    { return false }
func (t *githubListPRsTool) Risk() Risk        { return RiskNetwork }
func (t *githubListPRsTool) Label(a map[string]any) string {
	return "Listing pull requests"
}
func (t *githubListPRsTool) DoneLabel(a map[string]any) string {
	return "Listed pull requests"
}
func (t *githubListPRsTool) Description() string {
	return "List pull requests in the current repository with the gh CLI. Read-only."
}
func (t *githubListPRsTool) Schema() map[string]any {
	return object(map[string]any{
		"state": strProp("Filter by state: open, closed or all. Defaults to open."),
	})
}

func (t *githubListPRsTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	state := strings.ToLower(strings.TrimSpace(argString(args, "state")))
	switch state {
	case "open", "closed", "all", "merged":
	default:
		state = "open"
	}
	return githubResult(ctx, env, "pr", "list", "--state", state,
		"--limit", "30", "--json", githubPRFields), nil
}

type githubReviewPRTool struct{}

func (t *githubReviewPRTool) Name() string      { return "github_review_pr" }
func (t *githubReviewPRTool) Aliases() []string { return []string{"review_pr", "gh_pr_review"} }
func (t *githubReviewPRTool) Mutating() bool    { return true }
func (t *githubReviewPRTool) Risk() Risk        { return RiskCommand }
func (t *githubReviewPRTool) Label(a map[string]any) string {
	return fmt.Sprintf("Reviewing PR #%d", argInt(a, "number", 0, 0, 0))
}
func (t *githubReviewPRTool) DoneLabel(a map[string]any) string {
	return fmt.Sprintf("Reviewed PR #%d", argInt(a, "number", 0, 0, 0))
}
func (t *githubReviewPRTool) Description() string {
	return "Submit a review on a pull request: approve, request changes or comment, with the gh CLI."
}
func (t *githubReviewPRTool) Schema() map[string]any {
	return object(map[string]any{
		"number": intProp("Pull request number."),
		"state":  strProp("One of APPROVED, CHANGES_REQUESTED or COMMENTED."),
		"body":   strProp("Review body, Markdown."),
	}, "number", "state", "body")
}

func (t *githubReviewPRTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	number := argInt(args, "number", 0, 0, 0)
	if number <= 0 {
		return Result{Output: "number is required", IsError: true}, nil
	}
	body := argString(args, "body")
	flag := ""
	switch strings.ToUpper(strings.TrimSpace(argString(args, "state"))) {
	case "APPROVED", "APPROVE":
		flag = "--approve"
	case "CHANGES_REQUESTED", "REQUEST_CHANGES":
		flag = "--request-changes"
	case "COMMENTED", "COMMENT":
		flag = "--comment"
	default:
		return Result{Output: "state must be APPROVED, CHANGES_REQUESTED or COMMENTED", IsError: true}, nil
	}
	argv := []string{"pr", "review", fmt.Sprintf("%d", number), flag}
	if strings.TrimSpace(body) != "" {
		argv = append(argv, "--body", body)
	}
	return githubResult(ctx, env, argv...), nil
}

type githubCommentPRTool struct{}

func (t *githubCommentPRTool) Name() string      { return "github_comment_pr" }
func (t *githubCommentPRTool) Aliases() []string { return []string{"comment_pr", "gh_pr_comment"} }
func (t *githubCommentPRTool) Mutating() bool    { return true }
func (t *githubCommentPRTool) Risk() Risk        { return RiskCommand }
func (t *githubCommentPRTool) Label(a map[string]any) string {
	return fmt.Sprintf("Commenting on PR #%d", argInt(a, "number", 0, 0, 0))
}
func (t *githubCommentPRTool) DoneLabel(a map[string]any) string {
	return fmt.Sprintf("Commented on PR #%d", argInt(a, "number", 0, 0, 0))
}
func (t *githubCommentPRTool) Description() string {
	return "Post a comment on a pull request with the gh CLI."
}
func (t *githubCommentPRTool) Schema() map[string]any {
	return object(map[string]any{
		"number": intProp("Pull request number."),
		"body":   strProp("Comment body, Markdown."),
	}, "number", "body")
}

func (t *githubCommentPRTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	number := argInt(args, "number", 0, 0, 0)
	if number <= 0 {
		return Result{Output: "number is required", IsError: true}, nil
	}
	body := argString(args, "body")
	if strings.TrimSpace(body) == "" {
		return Result{Output: "body is required", IsError: true}, nil
	}
	return githubResult(ctx, env, "pr", "comment", fmt.Sprintf("%d", number), "--body", body), nil
}

type githubMergePRTool struct{}

func (t *githubMergePRTool) Name() string      { return "github_merge_pr" }
func (t *githubMergePRTool) Aliases() []string { return []string{"merge_pr", "gh_pr_merge"} }
func (t *githubMergePRTool) Mutating() bool    { return true }
func (t *githubMergePRTool) Risk() Risk        { return RiskCommand }
func (t *githubMergePRTool) Label(a map[string]any) string {
	return fmt.Sprintf("Merging PR #%d", argInt(a, "number", 0, 0, 0))
}
func (t *githubMergePRTool) DoneLabel(a map[string]any) string {
	return fmt.Sprintf("Merged PR #%d", argInt(a, "number", 0, 0, 0))
}
func (t *githubMergePRTool) Description() string {
	return "Merge a pull request with the gh CLI. This changes the remote repository and cannot be undone from here."
}
func (t *githubMergePRTool) Schema() map[string]any {
	return object(map[string]any{
		"number": intProp("Pull request number."),
		"method": strProp("Merge strategy: merge, squash or rebase. Defaults to merge."),
	}, "number")
}

func (t *githubMergePRTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	number := argInt(args, "number", 0, 0, 0)
	if number <= 0 {
		return Result{Output: "number is required", IsError: true}, nil
	}
	method := strings.ToLower(strings.TrimSpace(argString(args, "method")))
	switch method {
	case "squash":
		method = "--squash"
	case "rebase":
		method = "--rebase"
	default:
		method = "--merge"
	}
	return githubResult(ctx, env, "pr", "merge", fmt.Sprintf("%d", number), method), nil
}
