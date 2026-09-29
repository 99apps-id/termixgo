package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// checkpointPrefix marks stash entries owned by Termixgo. Anything else in
// the stash list belongs to the operator and is never touched: dropping or
// applying a foreign entry would destroy work this program did not create.
const checkpointPrefix = "termixgo checkpoint"

// checkpointKeep caps the auto-created entries. A checkpoint per turn would
// otherwise grow the stash list without bound on a long session.
const checkpointKeep = 10

// checkpointTimeout bounds one git invocation. Checkpointing is best effort
// before a turn, so a hung git must not cost the operator the turn itself.
const checkpointTimeout = 60 * time.Second

// Checkpoint is one saved working-tree snapshot, backed by a git stash entry.
type Checkpoint struct {
	// Ref is the stash ref, for example "stash@{0}".
	Ref string
	// Message is the operator or auto message, without the prefix.
	Message string
	// Head is the commit the tree stood on when the snapshot was taken.
	Head string
	// CreatedAt is parsed from the stash entry when available.
	CreatedAt time.Time
}

// CreateCheckpoint saves the working tree of a git workspace. On a clean
// tree there is nothing to save and the returned checkpoint carries no ref.
// On a tree that is not a git repository an error is returned.
func CreateCheckpoint(ctx context.Context, workspace, message string) (Checkpoint, error) {
	if strings.TrimSpace(workspace) == "" {
		return Checkpoint{}, fmt.Errorf("workspace is required")
	}
	if !isGitWorkspace(ctx, workspace) {
		return Checkpoint{}, fmt.Errorf("%s is not a git repository", workspace)
	}
	head, err := gitOutput(ctx, workspace, "rev-parse", "HEAD")
	if err != nil {
		return Checkpoint{}, fmt.Errorf("read HEAD: %w", err)
	}
	status, err := gitOutput(ctx, workspace, "status", "--porcelain=v1")
	if err != nil {
		return Checkpoint{}, fmt.Errorf("read status: %w", err)
	}
	if strings.TrimSpace(status) == "" {
		return Checkpoint{Head: head, Message: message}, nil
	}
	if strings.TrimSpace(message) == "" {
		message = "manual checkpoint"
	}
	entry := fmt.Sprintf("%s %s %s", checkpointPrefix, head, message)
	if _, err := gitOutput(ctx, workspace, "stash", "push", "--include-untracked", "-m", entry); err != nil {
		return Checkpoint{}, fmt.Errorf("stash the working tree: %w", err)
	}
	// The push leaves the tree clean; applying brings the work back while
	// keeping the stash entry as the snapshot. If the apply fails the tree
	// is left clean at HEAD and the error names the entry for recovery.
	ref, err := newestCheckpointRef(ctx, workspace)
	if err != nil {
		return Checkpoint{}, err
	}
	if _, err := gitOutput(ctx, workspace, "stash", "apply", ref); err != nil {
		return Checkpoint{}, fmt.Errorf("restore the working tree after %s: %w (the snapshot is kept)", ref, err)
	}
	return Checkpoint{Ref: ref, Message: message, Head: head}, nil
}

// ListCheckpoints returns the Termixgo-owned stash entries, newest first.
// Foreign entries are skipped: only messages carrying the prefix qualify.
func ListCheckpoints(ctx context.Context, workspace string) ([]Checkpoint, error) {
	if !isGitWorkspace(ctx, workspace) {
		return nil, fmt.Errorf("%s is not a git repository", workspace)
	}
	out, err := gitOutput(ctx, workspace, "stash", "list", "--format=%gd%x00%s%x00%cI")
	if err != nil {
		return nil, fmt.Errorf("list checkpoints: %w", err)
	}
	var checkpoints []Checkpoint
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, "\x00")
		if len(fields) != 3 {
			continue
		}
		ref, subject := strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1])
		// Git prefixes the subject with the branch ("On main: ..."), so the
		// marker can sit anywhere in the line rather than at its start.
		index := strings.Index(subject, checkpointPrefix)
		if index < 0 {
			continue
		}
		rest := strings.TrimSpace(subject[index+len(checkpointPrefix):])
		head, message := splitCheckpointSubject(rest)
		var created time.Time
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(fields[2])); err == nil {
			created = parsed
		}
		checkpoints = append(checkpoints, Checkpoint{Ref: ref, Message: message, Head: head, CreatedAt: created})
	}
	return checkpoints, nil
}

// RewindToCheckpoint restores the snapshot held by ref. Tracked files return
// to the checkpoint HEAD plus its stashed changes; untracked files created
// after the checkpoint are left alone rather than deleted. Commits made after
// the checkpoint are moved out of the way by the reset and survive only in
// the reflog, which is what makes a rewind a deliberate undo.
func RewindToCheckpoint(ctx context.Context, workspace, ref string) (Checkpoint, error) {
	target, err := findCheckpoint(ctx, workspace, ref)
	if err != nil {
		return Checkpoint{}, err
	}
	if target.Head == "" {
		return Checkpoint{}, fmt.Errorf("%s has no recorded HEAD and cannot be restored", ref)
	}
	if _, err := gitOutput(ctx, workspace, "reset", "--hard", target.Head); err != nil {
		return Checkpoint{}, fmt.Errorf("reset to %s: %w", target.Head, err)
	}
	// The snapshot holds untracked files too, and applying over a file the
	// agent created afterwards fails with "already exists, no checkout".
	// Clearing just the stashed untracked paths lets the apply restore them;
	// anything else untracked is left alone rather than deleted.
	clearStashUntracked(ctx, workspace, target.Ref)
	if _, err := gitOutput(ctx, workspace, "stash", "apply", target.Ref); err != nil {
		return Checkpoint{}, fmt.Errorf("apply %s: %w", target.Ref, err)
	}
	return target, nil
}

// RewindToLatest restores the newest Termixgo checkpoint.
func RewindToLatest(ctx context.Context, workspace string) (Checkpoint, error) {
	checkpoints, err := ListCheckpoints(ctx, workspace)
	if err != nil {
		return Checkpoint{}, err
	}
	if len(checkpoints) == 0 {
		return Checkpoint{}, fmt.Errorf("no checkpoints yet; create one with /checkpoint")
	}
	return RewindToCheckpoint(ctx, workspace, checkpoints[0].Ref)
}

// PruneCheckpoints drops all but the newest keep entries owned by Termixgo.
func PruneCheckpoints(ctx context.Context, workspace string, keep int) error {
	if keep < 1 {
		keep = 1
	}
	checkpoints, err := ListCheckpoints(ctx, workspace)
	if err != nil {
		return err
	}
	for _, extra := range checkpoints[min(keep, len(checkpoints)):] {
		if _, err := gitOutput(ctx, workspace, "stash", "drop", extra.Ref); err != nil {
			return fmt.Errorf("drop %s: %w", extra.Ref, err)
		}
		// Dropping renumbers the refs, so re-list before the next drop.
		return PruneCheckpoints(ctx, workspace, keep)
	}
	return nil
}

// AutoCheckpoint saves a best-effort snapshot before a turn and prunes the
// backlog. It never fails: a turn must not be lost because git was slow.
func AutoCheckpoint(ctx context.Context, workspace string) {
	timeoutCtx, cancel := context.WithTimeout(ctx, checkpointTimeout)
	defer cancel()
	if !isGitWorkspace(timeoutCtx, workspace) {
		return
	}
	if _, err := CreateCheckpoint(timeoutCtx, workspace, "auto before run"); err != nil {
		return
	}
	_ = PruneCheckpoints(timeoutCtx, workspace, checkpointKeep)
}

func findCheckpoint(ctx context.Context, workspace, ref string) (Checkpoint, error) {
	checkpoints, err := ListCheckpoints(ctx, workspace)
	if err != nil {
		return Checkpoint{}, err
	}
	for _, item := range checkpoints {
		if item.Ref == ref {
			return item, nil
		}
	}
	return Checkpoint{}, fmt.Errorf("%s is not a Termixgo checkpoint", ref)
}

func newestCheckpointRef(ctx context.Context, workspace string) (string, error) {
	checkpoints, err := ListCheckpoints(ctx, workspace)
	if err != nil {
		return "", err
	}
	if len(checkpoints) == 0 {
		return "", fmt.Errorf("the checkpoint entry was not found after stashing")
	}
	// The push just ran, so the newest entry is the one it created.
	return checkpoints[0].Ref, nil
}

// splitCheckpointSubject separates the embedded HEAD from the message. The
// subject follows the prefix as "<head> <message>"; a short or missing head
// means an older format and is kept as message text rather than rejected.
func splitCheckpointSubject(rest string) (head, message string) {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", ""
	}
	head = fields[0]
	if len(head) < 7 {
		return "", rest
	}
	for _, char := range head {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", rest
		}
	}
	return head, strings.TrimSpace(strings.TrimPrefix(rest, head))
}

// clearStashUntracked removes the working-tree files that a stash entry
// stored as untracked, so a later apply can restore them. Paths are confined
// to the workspace: anything escaping it is skipped rather than deleted.
func clearStashUntracked(ctx context.Context, workspace, ref string) {
	out, err := gitOutput(ctx, workspace, "ls-tree", "-r", "--name-only", ref+"^3", "--")
	if err != nil {
		return
	}
	base, err := filepath.Abs(workspace)
	if err != nil {
		return
	}
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		target := filepath.Join(base, filepath.FromSlash(name))
		relative, err := filepath.Rel(base, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if info, err := os.Lstat(target); err == nil && !info.IsDir() {
			_ = os.Remove(target)
			continue
		}
		if _, err := os.Lstat(target); err == nil {
			_ = os.RemoveAll(target)
		}
	}
}

func isGitWorkspace(ctx context.Context, workspace string) bool {
	command := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	command.Dir = workspace
	var buffer bytes.Buffer
	command.Stdout = &buffer
	command.Stderr = &buffer
	if err := command.Run(); err != nil {
		return false
	}
	return strings.TrimSpace(buffer.String()) == "true"
}

func gitOutput(ctx context.Context, workspace string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = workspace
	var buffer bytes.Buffer
	command.Stdout = &buffer
	command.Stderr = &buffer
	if err := command.Run(); err != nil {
		output := strings.TrimSpace(buffer.String())
		if output == "" {
			output = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), output)
	}
	return strings.TrimSpace(buffer.String()), nil
}
