package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// gitWorktreeTool manages linked working trees for parallel tasks. One task
// per worktree keeps two agent turns from stepping on each other's files.
type gitWorktreeTool struct{}

func (t *gitWorktreeTool) Name() string      { return "git_worktree" }
func (t *gitWorktreeTool) Aliases() []string { return []string{"worktree"} }
func (t *gitWorktreeTool) Mutating() bool    { return true }
func (t *gitWorktreeTool) Risk() Risk        { return RiskEdit }
func (t *gitWorktreeTool) Label(a map[string]any) string {
	return "Managing worktree " + Shorten(argString(a, "action"), 40)
}
func (t *gitWorktreeTool) DoneLabel(a map[string]any) string {
	return "Managed worktree " + Shorten(argString(a, "action"), 40)
}
func (t *gitWorktreeTool) Description() string {
	return "List, add or remove git worktrees for parallel tasks. A relative path is resolved next to the workspace, so fix-login becomes a sibling checkout. Use it when a second task must run without disturbing the current tree."
}
func (t *gitWorktreeTool) Schema() map[string]any {
	return object(map[string]any{
		"action": strProp("One of list, add or remove."),
		"path":   strProp("Worktree path for add or remove."),
		"branch": strProp("Branch for add; created when it does not exist yet."),
	}, "action")
}

func (t *gitWorktreeTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	switch strings.ToLower(strings.TrimSpace(argString(args, "action"))) {
	case "", "list":
		output, err := runGit(ctx, env, "worktree", "list", "--porcelain")
		if err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		if strings.TrimSpace(output) == "" {
			return Result{Output: "No worktrees."}, nil
		}
		return Result{Output: summarizeWorktrees(output)}, nil
	case "add":
		return worktreeAdd(ctx, env, args)
	case "remove", "rm":
		return worktreeRemove(ctx, env, args)
	default:
		return Result{Output: "action must be one of list, add or remove", IsError: true}, nil
	}
}

// worktreePath resolves the operator-facing path. Relative paths sit next to
// the workspace rather than inside it: "fix-login" becomes a sibling
// checkout, because a worktree nested in the checkout pollutes the very tree
// it was meant to leave alone.
func worktreePath(env *Env, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || filepath.IsAbs(raw) {
		return raw
	}
	return filepath.Join(filepath.Dir(env.Workspace), filepath.FromSlash(raw))
}

func worktreeAdd(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := strings.TrimSpace(argString(args, "path"))
	if raw == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	path := worktreePath(env, raw)
	argv := []string{"worktree", "add", path}
	if branch := strings.TrimSpace(argString(args, "branch")); branch != "" {
		argv = append(argv, "-b", branch)
	}
	if output, err := runGit(ctx, env, argv...); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	} else if strings.TrimSpace(output) != "" {
		return Result{Output: output}, nil
	}
	return Result{Output: fmt.Sprintf("Worktree ready at %s.", path)}, nil
}

func worktreeRemove(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := strings.TrimSpace(argString(args, "path"))
	if raw == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	path := worktreePath(env, raw)
	if output, err := runGit(ctx, env, "worktree", "remove", path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	} else if strings.TrimSpace(output) != "" {
		return Result{Output: output}, nil
	}
	// Git leaves an empty directory behind; removing it keeps the listing clean.
	_ = os.Remove(path)
	return Result{Output: fmt.Sprintf("Worktree %s removed.", raw)}, nil
}

func summarizeWorktrees(porcelain string) string {
	var lines []string
	for _, line := range strings.Split(porcelain, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "worktree ") {
			lines = append(lines, "  "+strings.TrimPrefix(line, "worktree "))
		} else if strings.HasPrefix(line, "branch ") && len(lines) > 0 {
			lines[len(lines)-1] += "  (" + strings.TrimPrefix(line, "branch refs/heads/") + ")"
		} else if line == "bare" && len(lines) > 0 {
			lines[len(lines)-1] += "  [bare]"
		}
	}
	if len(lines) == 0 {
		return "No worktrees."
	}
	return fmt.Sprintf("Worktrees (%d):\n%s", len(lines), strings.Join(lines, "\n"))
}
