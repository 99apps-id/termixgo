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

// gitForTestAllowFailure is gitForTest for a command that is expected to fail,
// such as naming the third parent of a stash entry that has only two.
func gitForTestAllowFailure(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	return string(output), err
}

func commitBaseForTest(t *testing.T, dir string) {
	t.Helper()
	writeForTest(t, dir, "main.go", "package main\n")
	gitForTest(t, dir, "add", "-A")
	gitForTest(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-qm", "base")
}

// TestCheckpointSkipsUntrackedWhenTheVolumeIsLarge pins the guard that keeps a
// dependency tree out of git's object store.
//
// A stash carrying untracked files copies them into the object store, where
// `git stash drop` cannot reclaim them. An auto-checkpoint of a workspace whose
// node_modules was not ignored yet is what put 387 MB into this repository for
// good, so the untracked side is left out once it is too large to be work.
func TestCheckpointSkipsUntrackedWhenTheVolumeIsLarge(t *testing.T) {
	dir := checkpointRepo(t)
	previous := checkpointUntrackedFileCap
	checkpointUntrackedFileCap = 3
	t.Cleanup(func() { checkpointUntrackedFileCap = previous })

	commitBaseForTest(t, dir)
	writeForTest(t, dir, "main.go", "package main // v2\n")
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dep-one.js", "dep-two.js", "dep-three.js", "dep-four.js"} {
		writeForTest(t, dir, filepath.Join("node_modules", name), "module.exports = 1\n")
	}

	checkpoint, err := CreateCheckpoint(context.Background(), dir, "auto before run")
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if checkpoint.Ref == "" {
		t.Fatalf("a tracked change must still produce a stash entry")
	}
	if !strings.Contains(checkpoint.Message, "untracked files left out") {
		t.Errorf("message = %q, want it to say the untracked side was left out", checkpoint.Message)
	}
	if tree, err := gitForTestAllowFailure(t, dir, "ls-tree", "-r", "--name-only", checkpoint.Ref+"^3"); err == nil && strings.TrimSpace(tree) != "" {
		t.Errorf("the untracked tree holds %q, want nothing copied", strings.TrimSpace(tree))
	}
	if strings.Contains(gitForTest(t, dir, "rev-list", "--objects", checkpoint.Ref), "node_modules") {
		t.Errorf("node_modules reached the object store")
	}
	// The tracked work is still the point of the snapshot, and the operator is
	// told about the gap where they would look for it.
	checkpoints, err := ListCheckpoints(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListCheckpoints: %v", err)
	}
	if len(checkpoints) != 1 || !strings.Contains(checkpoints[0].Message, "untracked files left out") {
		t.Errorf("checkpoints = %+v, want one entry that records the gap", checkpoints)
	}
}

// TestCheckpointSkipsUntrackedWhenTheBytesAreLarge covers the other half of the
// same bound: a handful of enormous files is as bad as a tree of many.
func TestCheckpointSkipsUntrackedWhenTheBytesAreLarge(t *testing.T) {
	dir := checkpointRepo(t)
	previous := checkpointUntrackedByteCap
	checkpointUntrackedByteCap = 16
	t.Cleanup(func() { checkpointUntrackedByteCap = previous })

	commitBaseForTest(t, dir)
	writeForTest(t, dir, "main.go", "package main // v2\n")
	writeForTest(t, dir, "bundle.js", strings.Repeat("x", 4096))

	checkpoint, err := CreateCheckpoint(context.Background(), dir, "auto before run")
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if checkpoint.Ref == "" {
		t.Fatalf("a tracked change must still produce a stash entry")
	}
	if !strings.Contains(checkpoint.Message, "untracked files left out") {
		t.Errorf("message = %q, want it to say the untracked side was left out", checkpoint.Message)
	}
}

// TestCheckpointKeepsUntrackedWhenTheVolumeIsSmall is the other side of the
// bound: protecting a newly created file is most of what an undo is for, so a
// small untracked set has to stay in the snapshot.
func TestCheckpointKeepsUntrackedWhenTheVolumeIsSmall(t *testing.T) {
	dir := checkpointRepo(t)
	commitBaseForTest(t, dir)
	writeForTest(t, dir, "main.go", "package main // v2\n")
	writeForTest(t, dir, "brand-new.go", "package main // new\n")

	checkpoint, err := CreateCheckpoint(context.Background(), dir, "auto before run")
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if checkpoint.Ref == "" {
		t.Fatalf("a dirty tree should produce a stash entry")
	}
	if strings.Contains(checkpoint.Message, "left out") {
		t.Errorf("message = %q, want a small untracked set to be kept", checkpoint.Message)
	}
	tree, err := gitForTestAllowFailure(t, dir, "ls-tree", "-r", "--name-only", checkpoint.Ref+"^3")
	if err != nil {
		t.Fatalf("the untracked tree should exist: %v\n%s", err, tree)
	}
	if !strings.Contains(tree, "brand-new.go") {
		t.Errorf("untracked tree = %q, want the new file in it", strings.TrimSpace(tree))
	}
}

// TestUntrackedVolumeMeasuredWithoutCountLimit proves the counting stops at the
// cap instead of measuring a whole dependency tree, and that it reports the
// reason rather than a number nobody can use.
func TestUntrackedVolumeMeasuredWithoutCountLimit(t *testing.T) {
	dir := checkpointRepo(t)
	previous := checkpointUntrackedFileCap
	checkpointUntrackedFileCap = 2
	t.Cleanup(func() { checkpointUntrackedFileCap = previous })
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		writeForTest(t, dir, name, "one\n")
	}
	files, _, tooBig := untrackedVolume(context.Background(), dir)
	if !tooBig {
		t.Errorf("tooBig = false, want true past a cap of 2 with 3 files")
	}
	if files != 3 {
		t.Errorf("files = %d, want the count that tripped the cap", files)
	}
}
