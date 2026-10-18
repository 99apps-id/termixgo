package agent

import (
	"context"
	"fmt"
	"strings"
)

// checkpointTool saves a working-tree snapshot before a risky change, so a
// bad edit can be undone with the rewind tool instead of by hand.
type checkpointTool struct{}

func (t *checkpointTool) Name() string      { return "checkpoint" }
func (t *checkpointTool) Aliases() []string { return []string{"snapshot"} }
func (t *checkpointTool) Mutating() bool    { return true }
func (t *checkpointTool) Risk() Risk        { return RiskEdit }
func (t *checkpointTool) Label(a map[string]any) string {
	return "Saving a checkpoint"
}
func (t *checkpointTool) DoneLabel(a map[string]any) string {
	return "Saved a checkpoint"
}
func (t *checkpointTool) Description() string {
	return "Save a snapshot of the git working tree before a risky change. Use it before a large refactor or a batch of edits, so the rewind tool can restore this exact state if the change goes wrong. No message argument is needed; say what the checkpoint is for in one short phrase."
}
func (t *checkpointTool) Schema() map[string]any {
	return object(map[string]any{
		"message": strProp("What the checkpoint is for, in a few words."),
	}, "message")
}

func (t *checkpointTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	checkpoint, err := CreateCheckpoint(ctx, env.Workspace, argString(args, "message"))
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if checkpoint.Ref == "" {
		return Result{Output: "Nothing to save: the working tree is clean."}, nil
	}
	return Result{Output: fmt.Sprintf("Checkpoint %s saved.", checkpoint.Ref)}, nil
}

// rewindTool restores a checkpoint created by the checkpoint tool,
// discarding the changes made since. It refuses unknown refs rather than
// running a bare git reset, which is what keeps a typo from wiping work.
type rewindTool struct{}

func (t *rewindTool) Name() string      { return "rewind" }
func (t *rewindTool) Aliases() []string { return []string{"restore_checkpoint", "undo_checkpoint"} }
func (t *rewindTool) Mutating() bool    { return true }
func (t *rewindTool) Risk() Risk        { return RiskEdit }
func (t *rewindTool) Label(a map[string]any) string {
	return "Rewinding to a checkpoint"
}
func (t *rewindTool) DoneLabel(a map[string]any) string {
	return "Rewound to a checkpoint"
}
func (t *rewindTool) Description() string {
	return "Restore the working tree to a checkpoint saved by the checkpoint tool, discarding later changes to tracked files. Prefer it over git_restore when the whole change set must go, for example after a refactor landed in the wrong direction. Commits made after the checkpoint leave the branch and survive only in the reflog."
}
func (t *rewindTool) Schema() map[string]any {
	return object(map[string]any{
		"ref": strProp("Checkpoint ref such as stash@{0}. Defaults to the newest checkpoint."),
	})
}

func (t *rewindTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	ref := strings.TrimSpace(argString(args, "ref"))
	var checkpoint Checkpoint
	var err error
	if ref == "" {
		checkpoint, err = RewindToLatest(ctx, env.Workspace)
	} else {
		checkpoint, err = RewindToCheckpoint(ctx, env.Workspace, ref)
	}
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	message := checkpoint.Message
	if strings.TrimSpace(message) == "" {
		message = checkpoint.Ref
	}
	return Result{Output: fmt.Sprintf("Restored %s (%s).", checkpoint.Ref, message)}, nil
}
