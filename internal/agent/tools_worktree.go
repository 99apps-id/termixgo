package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	return "List, add, remove, touch or prune git worktrees for parallel tasks. A relative path is resolved next to the workspace, so fix-login becomes a sibling checkout. Use it when a second task must run without disturbing the current tree. prune removes a tracked worktree that has gone idle and snapshots its tip to refs/termixgo/snapshots first."
}
func (t *gitWorktreeTool) Schema() map[string]any {
	return object(map[string]any{
		"action":  strProp("One of list, add, remove, touch or prune."),
		"path":    strProp("Worktree path for add, remove or touch."),
		"branch":  strProp("Branch for add; created when it does not exist yet."),
		"max_age": strProp("For prune: how long a worktree may sit idle, such as 168h. Default 168h."),
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
		summary := summarizeWorktrees(output)
		if tracked := trackedWorktreeLines(env.Workspace, time.Now()); tracked != "" {
			summary += "\n" + tracked
		}
		return Result{Output: summary}, nil
	case "add":
		return worktreeAdd(ctx, env, args)
	case "remove", "rm":
		return worktreeRemove(ctx, env, args)
	case "touch":
		return worktreeTouch(env, args)
	case "prune":
		return worktreePrune(ctx, env, args)
	default:
		return Result{Output: "action must be one of list, add, remove, touch or prune", IsError: true}, nil
	}
}

// trackedWorktreeLines reports the registered worktrees and how long they have
// been idle, so the operator can see what prune would reclaim.
func trackedWorktreeLines(workspace string, now time.Time) string {
	entries, err := loadWorktreeEntries(workspace)
	if err != nil || len(entries) == 0 {
		return ""
	}
	lines := []string{fmt.Sprintf("Tracked (%d):", len(entries))}
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("  %s  idle %s", entry.Name, shortAge(now.Sub(entry.LastUsedAt))))
	}
	return strings.Join(lines, "\n")
}

func worktreeTouch(env *Env, args map[string]any) (Result, error) {
	raw := strings.TrimSpace(argString(args, "path"))
	if raw == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	path := worktreePath(env, raw)
	if err := touchWorktree(env.Workspace, path, time.Now()); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Worktree %s marked as just used.", raw)}, nil
}

// defaultWorktreeMaxAge is how long a tracked worktree may sit idle before
// prune reclaims it.
const defaultWorktreeMaxAge = 7 * 24 * time.Hour

// worktreePrune removes tracked worktrees that have gone idle and hold no
// uncommitted work. A clean tip is snapshotted to refs/termixgo/snapshots
// first, so removing the checkout never loses a commit.
func worktreePrune(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	maxAge := defaultWorktreeMaxAge
	if raw := strings.TrimSpace(argString(args, "max_age")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < time.Hour {
			return Result{Output: "max_age must be a duration of at least one hour, such as 168h", IsError: true}, nil
		}
		maxAge = parsed
	}
	entries, err := loadWorktreeEntries(env.Workspace)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	now := time.Now()
	var removed, kept []string
	for _, entry := range entries {
		if now.Sub(entry.LastUsedAt) < maxAge {
			continue
		}
		if _, statErr := os.Stat(entry.Path); statErr != nil {
			// The checkout is already gone; drop the stale record.
			_ = forgetWorktree(env.Workspace, entry.Path)
			continue
		}
		dirty, err := runGit(ctx, env, "-C", entry.Path, "status", "--porcelain")
		if err != nil {
			kept = append(kept, entry.Name+" (could not inspect)")
			continue
		}
		if strings.TrimSpace(dirty) != "" {
			kept = append(kept, entry.Name+" (uncommitted changes)")
			continue
		}
		if head, err := runGit(ctx, env, "-C", entry.Path, "rev-parse", "HEAD"); err == nil {
			ref := "refs/termixgo/snapshots/" + snapshotName(entry.Name, now)
			_, _ = runGit(ctx, env, "update-ref", ref, strings.TrimSpace(head))
		}
		if _, err := runGit(ctx, env, "worktree", "remove", entry.Path); err != nil {
			kept = append(kept, entry.Name+" (remove failed)")
			continue
		}
		_ = os.Remove(entry.Path)
		_ = forgetWorktree(env.Workspace, entry.Path)
		removed = append(removed, entry.Name)
	}
	var lines []string
	if len(removed) > 0 {
		lines = append(lines, "Pruned: "+strings.Join(removed, ", "))
	}
	if len(kept) > 0 {
		lines = append(lines, "Kept: "+strings.Join(kept, ", "))
	}
	if len(lines) == 0 {
		return Result{Output: "Nothing to prune."}, nil
	}
	return Result{Output: strings.Join(lines, "\n")}, nil
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
		// Git only prints when it has something to say, such as preparing a
		// new branch; the record still has to be written.
		_ = recordWorktree(env.Workspace, worktreeEntry{
			Name:       filepath.Base(path),
			Path:       path,
			Branch:     strings.TrimSpace(argString(args, "branch")),
			CreatedAt:  time.Now(),
			LastUsedAt: time.Now(),
		})
		return Result{Output: output}, nil
	}
	_ = recordWorktree(env.Workspace, worktreeEntry{
		Name:       filepath.Base(path),
		Path:       path,
		Branch:     strings.TrimSpace(argString(args, "branch")),
		CreatedAt:  time.Now(),
		LastUsedAt: time.Now(),
	})
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
	_ = forgetWorktree(env.Workspace, path)
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
