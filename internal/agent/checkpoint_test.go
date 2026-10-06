package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func checkpointRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	gitForTest(t, dir, "init", "-q")
	gitForTest(t, dir, "config", "user.email", "test@example.com")
	gitForTest(t, dir, "config", "user.name", "test")
	gitForTest(t, dir, "commit", "--allow-empty", "-qm", "init")
	return dir
}

func writeForTest(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readForTest(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

func TestCreateCheckpointOutsideGitFails(t *testing.T) {
	ctx := context.Background()
	if _, err := CreateCheckpoint(ctx, t.TempDir(), "hello"); err == nil {
		t.Errorf("a non-git workspace must report an error")
	}
}

func TestCreateCheckpointOnCleanTreeHasNoRef(t *testing.T) {
	dir := checkpointRepo(t)
	ctx := context.Background()
	checkpoint, err := CreateCheckpoint(ctx, dir, "clean")
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if checkpoint.Ref != "" {
		t.Errorf("a clean tree needs no stash entry, got ref %q", checkpoint.Ref)
	}
	if checkpoint.Head == "" {
		t.Errorf("the HEAD should still be recorded")
	}
}

func TestCheckpointRoundTripRestoresTrackedFiles(t *testing.T) {
	dir := checkpointRepo(t)
	ctx := context.Background()
	writeForTest(t, dir, "main.go", "package main\n")
	gitForTest(t, dir, "add", "-A")
	gitForTest(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-qm", "base")

	writeForTest(t, dir, "main.go", "package main // v2\n")
	checkpoint, err := CreateCheckpoint(ctx, dir, "before risky edit")
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if checkpoint.Ref == "" {
		t.Fatalf("a dirty tree should produce a stash entry")
	}
	// The working tree must be untouched by the snapshot itself.
	// Git may normalize line endings on checkout, so compare loosely.
	if got := strings.ReplaceAll(readForTest(t, dir, "main.go"), "\r\n", "\n"); got != "package main // v2\n" {
		t.Fatalf("the snapshot changed the working tree: %q", got)
	}

	writeForTest(t, dir, "main.go", "package main // broken\n")
	restored, err := RewindToCheckpoint(ctx, dir, checkpoint.Ref)
	if err != nil {
		t.Fatalf("RewindToCheckpoint: %v", err)
	}
	if restored.Ref != checkpoint.Ref {
		t.Errorf("restored ref = %q, want %q", restored.Ref, checkpoint.Ref)
	}
	if got := strings.ReplaceAll(readForTest(t, dir, "main.go"), "\r\n", "\n"); got != "package main // v2\n" {
		t.Errorf("after rewind the file = %q, want the checkpoint content", got)
	}
}

func TestListCheckpointsIgnoresForeignStashes(t *testing.T) {
	dir := checkpointRepo(t)
	ctx := context.Background()
	writeForTest(t, dir, "a.txt", "ours\n")
	if _, err := CreateCheckpoint(ctx, dir, "ours"); err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	writeForTest(t, dir, "b.txt", "theirs\n")
	gitForTest(t, dir, "stash", "push", "--include-untracked", "-m", "someone else")

	checkpoints, err := ListCheckpoints(ctx, dir)
	if err != nil {
		t.Fatalf("ListCheckpoints: %v", err)
	}
	if len(checkpoints) != 1 {
		t.Fatalf("checkpoints = %d, want only the owned entry", len(checkpoints))
	}
	if checkpoints[0].Message != "ours" {
		t.Errorf("message = %q, want ours", checkpoints[0].Message)
	}
	if checkpoints[0].Head == "" {
		t.Errorf("the HEAD should be embedded in the entry")
	}
}

// A tree whose only change is Termixgo's own state directory has nothing to
// stash: the pathspec excludes it. The checkpoint must report no ref rather
// than apply the previous entry, which would bring stale work back.
func TestCreateCheckpointWithOnlyStateDirDoesNotApplyOldEntry(t *testing.T) {
	dir := checkpointRepo(t)
	ctx := context.Background()
	writeForTest(t, dir, "a.txt", "base\n")
	gitForTest(t, dir, "add", "-A")
	gitForTest(t, dir, "commit", "-qm", "base")

	writeForTest(t, dir, "a.txt", "stale\n")
	old, err := CreateCheckpoint(ctx, dir, "old")
	if err != nil {
		t.Fatalf("old checkpoint: %v", err)
	}
	if old.Ref == "" {
		t.Fatalf("a dirty tree should produce a stash entry")
	}
	gitForTest(t, dir, "checkout", "--", "a.txt")

	if err := os.MkdirAll(filepath.Join(dir, ".termixgo"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeForTest(t, dir, ".termixgo/search.db", "state\n")
	checkpoint, err := CreateCheckpoint(ctx, dir, "new")
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if checkpoint.Ref != "" {
		t.Errorf("nothing outside .termixgo changed, got ref %q", checkpoint.Ref)
	}
	if got := strings.ReplaceAll(readForTest(t, dir, "a.txt"), "\r\n", "\n"); got != "base\n" {
		t.Errorf("the old checkpoint was applied over the tree: a.txt = %q", got)
	}
}

func TestRewindUnknownRefIsRefused(t *testing.T) {
	dir := checkpointRepo(t)
	ctx := context.Background()
	if _, err := RewindToCheckpoint(ctx, dir, "stash@{9}"); err == nil {
		t.Errorf("an unknown ref must report an error")
	}
}

func TestRewindToLatestPicksTheNewest(t *testing.T) {
	dir := checkpointRepo(t)
	ctx := context.Background()
	writeForTest(t, dir, "f.txt", "v1\n")
	if _, err := CreateCheckpoint(ctx, dir, "first"); err != nil {
		t.Fatalf("first: %v", err)
	}
	writeForTest(t, dir, "f.txt", "v2\n")
	second, err := CreateCheckpoint(ctx, dir, "second")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	writeForTest(t, dir, "f.txt", "v3\n")
	restored, err := RewindToLatest(ctx, dir)
	if err != nil {
		t.Fatalf("RewindToLatest: %v", err)
	}
	if restored.Ref != second.Ref {
		t.Errorf("restored ref = %q, want the newest %q", restored.Ref, second.Ref)
	}
	if got := strings.ReplaceAll(readForTest(t, dir, "f.txt"), "\r\n", "\n"); got != "v2\n" {
		t.Errorf("after rewind the file = %q, want v2", got)
	}
}

func TestPruneCheckpointsKeepsTheNewest(t *testing.T) {
	dir := checkpointRepo(t)
	ctx := context.Background()
	for _, content := range []string{"one\n", "two\n", "three\n"} {
		writeForTest(t, dir, "f.txt", content)
		if _, err := CreateCheckpoint(ctx, dir, content); err != nil {
			t.Fatalf("CreateCheckpoint: %v", err)
		}
	}
	if err := PruneCheckpoints(ctx, dir, 2); err != nil {
		t.Fatalf("PruneCheckpoints: %v", err)
	}
	checkpoints, err := ListCheckpoints(ctx, dir)
	if err != nil {
		t.Fatalf("ListCheckpoints: %v", err)
	}
	if len(checkpoints) != 2 {
		t.Errorf("checkpoints = %d, want 2 kept", len(checkpoints))
	}
}

func TestFileCheckpointSavesAndRestoresWithoutGit(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeForTest(t, dir, "pkg/main.go", "package main // v1\n")
	store, err := CreateFileCheckpoint(dir, "before risky edit")
	if err != nil {
		t.Fatalf("CreateFileCheckpoint: %v", err)
	}
	if store.ID == "" {
		t.Fatalf("file checkpoint id is empty")
	}
	if len(store.Files) != 1 || store.Files[0] != "pkg/main.go" {
		t.Fatalf("file list = %v, want pkg/main.go", store.Files)
	}
	writeForTest(t, dir, "pkg/main.go", "package main // broken\n")
	restored, err := RewindFileCheckpoint(dir, store.ID)
	if err != nil {
		t.Fatalf("RewindFileCheckpoint: %v", err)
	}
	if restored.ID != store.ID {
		t.Errorf("restored id = %q, want %q", restored.ID, store.ID)
	}
	if got := readForTest(t, dir, "pkg/main.go"); got != "package main // v1\n" {
		t.Errorf("restored file = %q, want v1", got)
	}
	created := filepath.Join(fileCheckpointDir(dir), store.ID, "created-after.txt")
	if err := os.WriteFile(created, []byte("new\n"), 0o644); err != nil {
		t.Fatalf("create extra file: %v", err)
	}
	writeForTest(t, dir, "pkg/main.go", "package main // v2\n")
	if _, err := RewindFileCheckpoint(dir, store.ID); err != nil {
		t.Fatalf("second RewindFileCheckpoint: %v", err)
	}
	if got := readForTest(t, dir, "pkg/main.go"); got != "package main // v1\n" {
		t.Errorf("restored file = %q, want v1 after second rewind", got)
	}
	if _, err := os.Stat(created); err != nil {
		t.Errorf("created-after file must remain intact after rewind: %v", err)
	}
}
