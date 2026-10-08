package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/search"
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

// checkpointUntrackedFileCap and checkpointUntrackedByteCap bound the untracked
// side of a snapshot. They are variables so a test can lower them.
//
// A stash that carries untracked files copies them into git's object store,
// where they stay until the last ref that reaches them is gone: `git stash
// drop` does not give the space back, and only a history rewrite does. One
// auto-checkpoint of a workspace whose node_modules was not ignored yet put
// 387 MB of it into this repository for good. Gitignored junk costs nothing
// here, because `--include-untracked` skips ignored files, so what is measured
// is the untracked set that is not ignored, which is exactly what would be
// copied.
var (
	checkpointUntrackedFileCap = 5000
	checkpointUntrackedByteCap = int64(64 << 20)
)

// untrackedVolume measures the untracked, non-ignored files a stash would copy.
// Counting stops as soon as a cap is passed: the answer is already known, and a
// dependency tree can hold millions of files.
func untrackedVolume(ctx context.Context, workspace string) (files int, bytes int64, tooBig bool) {
	out, err := gitOutput(ctx, workspace, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		// A listing that cannot be read is a reason to keep the snapshot small.
		return 0, 0, true
	}
	for _, name := range strings.Split(out, "\x00") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		files++
		if files > checkpointUntrackedFileCap {
			return files, bytes, true
		}
		if info, statErr := os.Lstat(filepath.Join(workspace, filepath.FromSlash(name))); statErr == nil && !info.IsDir() {
			bytes += info.Size()
			if bytes > checkpointUntrackedByteCap {
				return files, bytes, true
			}
		}
	}
	return files, bytes, false
}

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
	// Termixgo's own state directory is excluded from the snapshot. It holds the
	// full-text index, its WAL and the error journal, which are state rather than
	// the operator's work, and stashing them makes a stash apply collide with the
	// live files. The pathspec covers the repository root; an excluded path that
	// does not exist is not an error for git. Status reads through the same
	// pathspec: a tree dirty only under .termixgo has nothing to stash, and
	// treating it as dirty would push nothing and then apply an older entry.
	pathspec := []string{"--", ".", ":(exclude).termixgo"}
	status, err := gitOutput(ctx, workspace, append([]string{"status", "--porcelain=v1"}, pathspec...)...)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("read status: %w", err)
	}
	if strings.TrimSpace(status) == "" {
		return Checkpoint{Head: head, Message: message}, nil
	}
	if strings.TrimSpace(message) == "" {
		message = "manual checkpoint"
	}
	// Untracked files are a different question from the pathspec above. Git
	// honors a pathspec exclusion for the tracked side but not for the
	// untracked one: `git stash push -u -- . ':(exclude)node_modules'` still
	// copies node_modules. The volume is therefore measured and the whole
	// untracked side is left out when it is a dependency or build tree.
	includeUntracked := true
	if files, bytes, tooBig := untrackedVolume(ctx, workspace); tooBig {
		includeUntracked = false
		message += fmt.Sprintf(" (untracked files left out: %d files, %d MB; ignore generated trees such as node_modules to keep them out for good)", files, bytes>>20)
	}
	entry := fmt.Sprintf("%s %s %s", checkpointPrefix, head, message)
	before := stashTop(ctx, workspace)
	pushArgs := []string{"stash", "push", "-m", entry}
	if includeUntracked {
		pushArgs = append(pushArgs, "--include-untracked")
	}
	pushArgs = append(pushArgs, pathspec...)
	if _, err := gitOutput(ctx, workspace, pushArgs...); err != nil {
		return Checkpoint{}, fmt.Errorf("stash the working tree: %w", err)
	}
	// "No local changes to save" exits 0 without creating an entry. Applying
	// the newest checkpoint then would replay an older snapshot over the
	// tree, so the push only counts when the stash top actually moved.
	if after := stashTop(ctx, workspace); after == "" || after == before {
		return Checkpoint{Head: head, Message: message}, nil
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

// stashTop is the commit refs/stash points at, or "" when the stash is empty.
func stashTop(ctx context.Context, workspace string) string {
	top, err := gitOutput(ctx, workspace, "rev-parse", "-q", "--verify", "refs/stash")
	if err != nil {
		return ""
	}
	return top
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
	// Dropping renumbers the refs, so the list is re-read before every drop
	// rather than trusting the positions taken from the first read.
	for {
		checkpoints, err := ListCheckpoints(ctx, workspace)
		if err != nil {
			return err
		}
		if len(checkpoints) <= keep {
			return nil
		}
		if _, err := gitOutput(ctx, workspace, "stash", "drop", checkpoints[keep].Ref); err != nil {
			return fmt.Errorf("drop %s: %w", checkpoints[keep].Ref, err)
		}
	}
}

// AutoCheckpoint saves a best-effort snapshot before a turn and prunes the
// backlog. It never fails: a turn must not be lost because git was slow.
// Outside a git repository the file-level snapshot store takes over, so a
// turn in a plain folder is still undoable with /rewind.
func AutoCheckpoint(ctx context.Context, workspace string) {
	timeoutCtx, cancel := context.WithTimeout(ctx, checkpointTimeout)
	defer cancel()
	if !isGitWorkspace(timeoutCtx, workspace) {
		_, _ = CreateFileCheckpoint(timeoutCtx, workspace, "auto before run")
		_ = PruneFileCheckpoints(workspace, checkpointKeep)
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

func isGitWorkspace(_ context.Context, workspace string) bool {
	return isGitRepository(workspace)
}

// File-level snapshots for workspaces without git. A stash entry needs a
// repository; a plain folder gets copies of the files a turn actually
// touches, stored under .termixgo/snapshots/<id>/ with a manifest. Edits,
// writes, patches and deletes register their targets before writing, so the
// snapshot holds the pre-change bytes, and rewind restores exactly those
// files while leaving everything created since alone.

// fileCheckpointDir is the snapshot store inside the workspace state
// directory. The search walk already skips .termixgo, and checkpoint state
// is not content, so it belongs there rather than beside the work.
func fileCheckpointDir(workspace string) string {
	return filepath.Join(workspace, ".termixgo", "snapshots")
}

// snapshotFileCap bounds one stored file. A snapshot is an undo buffer, not
// a backup: a file larger than this is skipped rather than copied, and the
// manifest says so, so a rewind never silently restores a partial file.
const snapshotFileCap = 4 * 1024 * 1024

// FileCheckpoint is one file-level snapshot.
type FileCheckpoint struct {
	ID        string    `json:"id"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
	Files     []string  `json:"files"`
	Skipped   []string  `json:"skipped,omitempty"`
	Restored  bool      `json:"restored,omitempty"`
}

// fileCheckpointRef renders the stable user-facing ref for a snapshot.
func fileCheckpointRef(id string) string { return "file:" + id }

// SnapshotFile records the current bytes of path before a mutating tool
// writes it. It is a no-op for paths outside the workspace, for missing
// files (there is nothing to restore), and for oversized files, which are
// named in the manifest instead of copied. Repeated calls for one path keep
// the first bytes: the snapshot is the state before the turn, not before
// each edit.
func SnapshotFile(workspace, path string) {
	if strings.TrimSpace(workspace) == "" || strings.TrimSpace(path) == "" {
		return
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return
	}
	if err := checkWorkspacePath(&Env{Workspace: workspace}, absolute); err != nil {
		return
	}
	info, err := os.Stat(absolute)
	if err != nil || info.IsDir() {
		return
	}
	relative, err := filepath.Rel(workspace, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return
	}
	// Never snapshot the snapshot store itself or the search database: the
	// former would recurse, the latter is a live SQLite file.
	slash := filepath.ToSlash(relative)
	if slash == ".termixgo" || strings.HasPrefix(slash, ".termixgo/") {
		return
	}
	store := currentFileSnapshot(workspace)
	if store == nil {
		return
	}
	for _, have := range store.Files {
		if have == slash {
			return
		}
	}
	for _, have := range store.Skipped {
		if have == slash {
			return
		}
	}
	if info.Size() > snapshotFileCap {
		store.Skipped = append(store.Skipped, slash)
		_ = writeFileManifest(workspace, store)
		return
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return
	}
	target := filepath.Join(fileCheckpointDir(workspace), store.ID, filepath.FromSlash(slash))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return
	}
	store.Files = append(store.Files, slash)
	_ = writeFileManifest(workspace, store)
}

// currentFileSnapshot returns the snapshot being collected for this turn,
// creating it on first use. One snapshot per turn is what makes rewind match
// the turn boundary: every file the turn touched returns to its pre-turn
// bytes at once.
func currentFileSnapshot(workspace string) *FileCheckpoint {
	dir := fileCheckpointDir(workspace)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
				return nil
			}
		} else {
			return nil
		}
		entries = nil
	}
	// Reuse today's in-progress snapshot when the turn already started one;
	// otherwise open a fresh id. The manifest's file list is the membership
	// test: a snapshot that already holds restored files from a rewind is
	// finished and must not be appended to.
	var newest *FileCheckpoint
	var newestTime time.Time
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		store := readFileManifest(workspace, entry.Name())
		if store == nil {
			continue
		}
		if store.CreatedAt.After(newestTime) {
			newestTime = store.CreatedAt
			newest = store
		}
	}
	if newest != nil && time.Since(newest.CreatedAt) < time.Hour && !newest.restored() {
		return newest
	}
	store := &FileCheckpoint{ID: newSnapshotID(), Message: "auto before run", CreatedAt: time.Now()}
	if err := writeFileManifest(workspace, store); err != nil {
		return nil
	}
	return store
}

// restored marks a snapshot that a rewind already consumed. Appending new
// files to it afterwards would mix pre-turn bytes from two different turns
// under one id.
func (s FileCheckpoint) restored() bool { return s.Restored }

func newSnapshotID() string {
	var buffer [8]byte
	sum := sha256.Sum256([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	copy(buffer[:], sum[:8])
	return hex.EncodeToString(buffer[:])
}

func snapshotManifestPath(workspace, id string) string {
	return filepath.Join(fileCheckpointDir(workspace), id, "manifest.json")
}

func readFileManifest(workspace, id string) *FileCheckpoint {
	data, err := os.ReadFile(snapshotManifestPath(workspace, id))
	if err != nil {
		return nil
	}
	var store FileCheckpoint
	if err := json.Unmarshal(data, &store); err != nil {
		return nil
	}
	if strings.TrimSpace(store.ID) == "" {
		store.ID = id
	}
	return &store
}

func writeFileManifest(workspace string, store *FileCheckpoint) error {
	if err := os.MkdirAll(filepath.Join(fileCheckpointDir(workspace), store.ID), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(snapshotManifestPath(workspace, store.ID), append(data, '\n'), 0o600)
}

func snapshotFiles(workspace string, store *FileCheckpoint) error {
	root := filepath.Join(fileCheckpointDir(workspace), store.ID)
	for _, relative := range store.Files {
		source := filepath.Join(workspace, filepath.FromSlash(relative))
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// fileCheckpointMaxFiles and fileCheckpointMaxBytes bound one file-level
// snapshot. A plain folder is snapshotted by walking it, so a filesystem root
// or a huge generated tree would otherwise be walked and copied on every turn,
// which stalls the turn before the model is even called. The scan stops at a
// cap, names an oversized file instead of copying it, and records the
// truncation, so the snapshot stays a bounded undo buffer rather than an
// archive of the whole tree.
const (
	fileCheckpointMaxFiles = 2000
	fileCheckpointMaxBytes = 64 * 1024 * 1024
)

// CreateFileCheckpoint opens a named file-level snapshot. Outside git this is
// what the auto checkpoint and the checkpoint tool use; inside git the stash
// path is used instead. The walk honours ctx and skips generated and state
// trees, so a checkpoint never scans a whole filesystem root.
func CreateFileCheckpoint(ctx context.Context, workspace, message string) (FileCheckpoint, error) {
	if strings.TrimSpace(workspace) == "" {
		return FileCheckpoint{}, fmt.Errorf("workspace is required")
	}
	if strings.TrimSpace(message) == "" {
		message = "manual checkpoint"
	}
	var files, skipped []string
	stored := 0
	truncated := false
	statePrefixes := []string{".termixgo" + string(filepath.Separator), ".termigo" + string(filepath.Separator)}
	_ = filepath.WalkDir(workspace, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			truncated = true
			return fs.SkipAll
		}
		if d.IsDir() {
			if path == workspace {
				return nil
			}
			name := d.Name()
			if name == ".termixgo" || name == ".termigo" || search.IsSkippedDir(name) {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(workspace, path)
		if err != nil {
			return nil
		}
		for _, prefix := range statePrefixes {
			if strings.HasPrefix(rel, prefix) {
				return nil
			}
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if info.Size() > snapshotFileCap {
			skipped = append(skipped, filepath.ToSlash(rel))
			return nil
		}
		if len(files) >= fileCheckpointMaxFiles || stored+int(info.Size()) > fileCheckpointMaxBytes {
			truncated = true
			return fs.SkipAll
		}
		stored += int(info.Size())
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	store := &FileCheckpoint{ID: newSnapshotID(), Message: message, CreatedAt: time.Now(), Files: files, Skipped: skipped}
	if truncated {
		store.Message += " [snapshot truncated: workspace too large]"
	}
	if err := writeFileManifest(workspace, store); err != nil {
		return FileCheckpoint{}, err
	}
	if err := snapshotFiles(workspace, store); err != nil {
		return FileCheckpoint{}, err
	}
	return *store, nil
}

// ListFileCheckpoints returns the file-level snapshots, newest first.
func ListFileCheckpoints(workspace string) ([]FileCheckpoint, error) {
	entries, err := os.ReadDir(fileCheckpointDir(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []FileCheckpoint
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if store := readFileManifest(workspace, entry.Name()); store != nil {
			out = append(out, *store)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// RewindFileCheckpoint restores every file a snapshot holds to its stored
// bytes. Files created after the snapshot are left alone; files the snapshot
// skipped as oversized are named rather than touched.
func RewindFileCheckpoint(workspace, id string) (FileCheckpoint, error) {
	store := readFileManifest(workspace, strings.TrimPrefix(id, "file:"))
	if store == nil {
		return FileCheckpoint{}, fmt.Errorf("%s is not a file checkpoint", fileCheckpointRef(strings.TrimPrefix(id, "file:")))
	}
	var failed []string
	for _, relative := range store.Files {
		target := filepath.Join(workspace, filepath.FromSlash(relative))
		if err := checkWorkspacePath(&Env{Workspace: workspace}, target); err != nil {
			failed = append(failed, relative)
			continue
		}
		data, err := os.ReadFile(filepath.Join(fileCheckpointDir(workspace), store.ID, filepath.FromSlash(relative)))
		if err != nil {
			failed = append(failed, relative)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			failed = append(failed, relative)
			continue
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			failed = append(failed, relative)
			continue
		}
		if info, statErr := os.Lstat(target); statErr == nil && info.Mode().IsRegular() {
			_ = os.Chmod(target, info.Mode()&os.ModePerm)
		}
	}
	store.Restored = true
	_ = writeFileManifest(workspace, store)
	if len(failed) > 0 {
		return *store, fmt.Errorf("could not restore %s", strings.Join(failed, ", "))
	}
	return *store, nil
}

// PruneFileCheckpoints drops all but the newest keep snapshots.
func PruneFileCheckpoints(workspace string, keep int) error {
	if keep < 1 {
		keep = 1
	}
	stores, err := ListFileCheckpoints(workspace)
	if err != nil {
		return err
	}
	for index := keep; index < len(stores); index++ {
		_ = os.RemoveAll(filepath.Join(fileCheckpointDir(workspace), stores[index].ID))
	}
	return nil
}

var _ = bytes.MinRead

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
