package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitEnv builds an environment with a real repository in it.
//
// Git is configured locally rather than globally, so the test never reads or
// writes the developer's own git identity.
func gitEnv(t *testing.T) *Env {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true}
	git(t, env, "init", "-b", "main")
	git(t, env, "config", "user.email", "termixgo@example.invalid")
	git(t, env, "config", "user.name", "Termixgo Test")
	git(t, env, "config", "commit.gpgsign", "false")
	// Line-ending translation would make a restored file differ from what was
	// written, which is a property of the test machine rather than of git.
	git(t, env, "config", "core.autocrlf", "false")
	return env
}

// git runs a setup command directly, failing the test on error.
func git(t *testing.T, env *Env, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = env.Workspace
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func writeTestFile(t *testing.T, env *Env, name, content string) {
	t.Helper()
	path := filepath.Join(env.Workspace, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitStatusReportsTheBranchAndChanges(t *testing.T) {
	env := gitEnv(t)
	writeTestFile(t, env, "new.txt", "hello\n")

	result, err := (&gitStatusTool{}).Run(context.Background(), env, map[string]any{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("status failed: %s", result.Output)
	}
	if !strings.Contains(result.Output, "new.txt") {
		t.Errorf("an untracked file should appear: %q", result.Output)
	}
	if !strings.Contains(result.Output, "main") {
		t.Errorf("the branch should appear: %q", result.Output)
	}
}

func TestGitStatusNamesACleanTreeWithItsBranch(t *testing.T) {
	env := gitEnv(t)
	git(t, env, "commit", "--allow-empty", "-m", "init")

	result, _ := (&gitStatusTool{}).Run(context.Background(), env, map[string]any{})
	if !strings.Contains(result.Output, "clean") {
		t.Errorf("a clean tree should be described as clean: %q", result.Output)
	}
	if !strings.Contains(result.Output, "main") {
		t.Errorf("the branch should still be reported: %q", result.Output)
	}
}

func TestGitToolsRefuseOutsideARepository(t *testing.T) {
	workspace := t.TempDir()
	env := &Env{Workspace: workspace, Todos: NewTodoStore()}

	for _, tool := range []Tool{&gitStatusTool{}, &gitDiffTool{}, &gitLogTool{}} {
		result, err := tool.Run(context.Background(), env, map[string]any{})
		if err != nil {
			t.Fatalf("%s: %v", tool.Name(), err)
		}
		if !result.IsError {
			t.Errorf("%s should refuse outside a repository", tool.Name())
		}
		if !strings.Contains(result.Output, "not a git repository") {
			t.Errorf("%s gave an unclear refusal: %q", tool.Name(), result.Output)
		}
	}
}

func TestGitDiffShowsTrackedChanges(t *testing.T) {
	env := gitEnv(t)
	writeTestFile(t, env, "file.txt", "original\n")
	git(t, env, "add", "file.txt")
	git(t, env, "commit", "-m", "add file")
	writeTestFile(t, env, "file.txt", "changed\n")

	result, err := (&gitDiffTool{}).Run(context.Background(), env, map[string]any{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.Output, "-original") || !strings.Contains(result.Output, "+changed") {
		t.Errorf("the diff should show both sides: %q", result.Output)
	}
}

func TestGitDiffStagedAndPathFilter(t *testing.T) {
	env := gitEnv(t)
	writeTestFile(t, env, "one.txt", "a\n")
	writeTestFile(t, env, "two.txt", "b\n")
	git(t, env, "add", ".")
	git(t, env, "commit", "-m", "add both")

	writeTestFile(t, env, "one.txt", "a edited\n")
	writeTestFile(t, env, "two.txt", "b edited\n")
	git(t, env, "add", "one.txt")

	staged, _ := (&gitDiffTool{}).Run(context.Background(), env, map[string]any{"staged": true})
	if !strings.Contains(staged.Output, "one.txt") {
		t.Errorf("the staged diff should include one.txt: %q", staged.Output)
	}
	if strings.Contains(staged.Output, "two.txt") {
		t.Errorf("two.txt was not staged and must not appear: %q", staged.Output)
	}

	limited, _ := (&gitDiffTool{}).Run(context.Background(), env, map[string]any{"path": "two.txt"})
	if !strings.Contains(limited.Output, "two.txt") || strings.Contains(limited.Output, "one.txt") {
		t.Errorf("the path filter did not apply: %q", limited.Output)
	}
}

func TestGitDiffWithNoChanges(t *testing.T) {
	env := gitEnv(t)
	git(t, env, "commit", "--allow-empty", "-m", "init")
	result, _ := (&gitDiffTool{}).Run(context.Background(), env, map[string]any{})
	if !strings.Contains(result.Output, "No changes") {
		t.Errorf("expected a no-changes message, got %q", result.Output)
	}
}

func TestGitLogListsCommitsNewestFirst(t *testing.T) {
	env := gitEnv(t)
	writeTestFile(t, env, "a.txt", "1\n")
	git(t, env, "add", ".")
	git(t, env, "commit", "-m", "first commit")
	writeTestFile(t, env, "b.txt", "2\n")
	git(t, env, "add", ".")
	git(t, env, "commit", "-m", "second commit")

	result, err := (&gitLogTool{}).Run(context.Background(), env, map[string]any{"limit": 10})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	second := strings.Index(result.Output, "second commit")
	first := strings.Index(result.Output, "first commit")
	if second < 0 || first < 0 {
		t.Fatalf("both commits should appear: %q", result.Output)
	}
	if second > first {
		t.Errorf("commits should be newest first: %q", result.Output)
	}
}

func TestGitShowDefaultsToHead(t *testing.T) {
	env := gitEnv(t)
	writeTestFile(t, env, "a.txt", "content\n")
	git(t, env, "add", ".")
	git(t, env, "commit", "-m", "the subject line")

	result, err := (&gitShowTool{}).Run(context.Background(), env, map[string]any{"stat": true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.Output, "the subject line") {
		t.Errorf("show should describe HEAD: %q", result.Output)
	}
}

// TestGitShowTreatsAnOptionLikeRefAsARevision keeps a read-only tool from
// writing outside the workspace. A ref that starts with a dash used to reach
// git as an option, and `--output=` wrote the patch to an arbitrary file.
func TestGitShowTreatsAnOptionLikeRefAsARevision(t *testing.T) {
	env := gitEnv(t)
	git(t, env, "commit", "--allow-empty", "-m", "base")
	outside := filepath.Join(t.TempDir(), "outside.txt")

	result, err := (&gitShowTool{}).Run(context.Background(), env, map[string]any{"ref": "--output=" + outside})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsError {
		t.Fatalf("an option-like ref should be refused as an unknown revision, got %q", result.Output)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("git_show wrote outside the workspace: %s was created", outside)
	}
}

// TestGitBranchTreatsAnOptionLikeNameAsABranch keeps a switch target from
// reaching git as an option: a name such as -f would force-switch and discard
// local changes.
func TestGitBranchTreatsAnOptionLikeNameAsABranch(t *testing.T) {
	env := gitEnv(t)
	git(t, env, "commit", "--allow-empty", "-m", "base")

	result, err := (&gitBranchTool{}).Run(context.Background(), env, map[string]any{"name": "-f"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsError {
		t.Fatalf("an option-like branch name should be refused, got %q", result.Output)
	}

	resultCreate, err := (&gitBranchTool{}).Run(context.Background(), env, map[string]any{"name": "-f", "create": true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !resultCreate.IsError {
		t.Fatalf("an option-like branch create should be refused, got %q", resultCreate.Output)
	}
}

// TestGitBlameRefusesAnOptionLikeRevision keeps a model-supplied revision out
// of git's option position while a real revision still works.
func TestGitBlameRefusesAnOptionLikeRevision(t *testing.T) {
	env := gitEnv(t)
	if err := os.WriteFile(filepath.Join(env.Workspace, "f.txt"), []byte("a\nb\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	git(t, env, "add", "f.txt")
	git(t, env, "commit", "-m", "add f")

	bad, err := (&gitBlameTool{}).Run(context.Background(), env, map[string]any{"path": "f.txt", "revision": "-f"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !bad.IsError {
		t.Fatalf("an option-like revision should be refused, got %q", bad.Output)
	}

	good, err := (&gitBlameTool{}).Run(context.Background(), env, map[string]any{"path": "f.txt", "revision": "HEAD"})
	if err != nil || good.IsError {
		t.Fatalf("HEAD blame failed: %+v err=%v", good, err)
	}
	if strings.TrimSpace(good.Output) == "" {
		t.Errorf("HEAD blame returned no output")
	}
}

func TestGitAddStagesEverything(t *testing.T) {
	env := gitEnv(t)
	git(t, env, "commit", "--allow-empty", "-m", "init")
	writeTestFile(t, env, "fresh.txt", "new\n")

	result, err := (&gitAddTool{}).Run(context.Background(), env, map[string]any{"all_changes": true})
	if err != nil || result.IsError {
		t.Fatalf("git_add failed: %+v err=%v", result, err)
	}

	staged, _ := (&gitDiffTool{}).Run(context.Background(), env, map[string]any{"staged": true})
	if !strings.Contains(staged.Output, "fresh.txt") {
		t.Errorf("the file was not staged: %q", staged.Output)
	}
}

func TestGitAddNeedsPathsOrTheAllFlag(t *testing.T) {
	env := gitEnv(t)
	result, _ := (&gitAddTool{}).Run(context.Background(), env, map[string]any{})
	if !result.IsError {
		t.Errorf("staging nothing should be refused")
	}
	if !strings.Contains(result.Output, "all_changes") {
		t.Errorf("the refusal should mention the flag that would work: %q", result.Output)
	}
}

func TestGitCommitRecordsTheMessage(t *testing.T) {
	env := gitEnv(t)
	writeTestFile(t, env, "work.txt", "done\n")

	// The file is new, so all_changes has to stage it. `git commit --all`
	// alone would not, which is exactly the bug this asserts against.
	result, err := (&gitCommitTool{}).Run(context.Background(), env, map[string]any{
		"message":     "add the work file",
		"all_changes": true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("commit failed: %s", result.Output)
	}

	log, _ := (&gitLogTool{}).Run(context.Background(), env, map[string]any{})
	if !strings.Contains(log.Output, "add the work file") {
		t.Errorf("the commit is missing from the log: %q", log.Output)
	}
}

func TestGitCommitRequiresAMessage(t *testing.T) {
	env := gitEnv(t)
	result, _ := (&gitCommitTool{}).Run(context.Background(), env, map[string]any{})
	if !result.IsError {
		t.Errorf("an empty message must be refused")
	}
}

// TestGitCommitMessageIsNotInterpreted is the injection guard. The message and
// the paths come from the model, and git runs with explicit argv rather than
// through a shell, so shell metacharacters must stay literal text.
func TestGitCommitMessageIsNotInterpreted(t *testing.T) {
	env := gitEnv(t)
	writeTestFile(t, env, "victim.txt", "safe\n")
	git(t, env, "add", ".")
	git(t, env, "commit", "-m", "base")

	// A file whose name would be a command if a shell were involved.
	hostile := "weird;touch PWNED.txt"
	writeTestFile(t, env, hostile, "x\n")

	result, err := (&gitAddTool{}).Run(context.Background(), env, map[string]any{"paths": []any{hostile}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("a filename with a semicolon is legal and should stage: %s", result.Output)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace, "PWNED.txt")); err == nil {
		t.Fatalf("the file name was interpreted as a command")
	}

	message := "fix: rename; rm -rf / # not a command"
	committed, err := (&gitCommitTool{}).Run(context.Background(), env, map[string]any{
		"message": message, "all_changes": true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if committed.IsError {
		t.Fatalf("commit failed: %s", committed.Output)
	}
	log, _ := (&gitLogTool{}).Run(context.Background(), env, map[string]any{})
	if !strings.Contains(log.Output, "not a command") {
		t.Errorf("the message should be stored verbatim: %q", log.Output)
	}
}

func TestGitRestoreDiscardsWorkingTreeChanges(t *testing.T) {
	env := gitEnv(t)
	writeTestFile(t, env, "code.txt", "good\n")
	git(t, env, "add", ".")
	git(t, env, "commit", "-m", "base")
	writeTestFile(t, env, "code.txt", "bad\n")

	result, err := (&gitRestoreTool{}).Run(context.Background(), env, map[string]any{"paths": []any{"code.txt"}})
	if err != nil || result.IsError {
		t.Fatalf("restore failed: %+v err=%v", result, err)
	}

	content, err := os.ReadFile(filepath.Join(env.Workspace, "code.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "good\n" {
		t.Errorf("the file was not restored: %q", content)
	}
}

func TestGitRestoreUnstagesWithoutLosingWork(t *testing.T) {
	env := gitEnv(t)
	writeTestFile(t, env, "code.txt", "good\n")
	git(t, env, "add", ".")
	git(t, env, "commit", "-m", "base")
	writeTestFile(t, env, "code.txt", "edited\n")
	git(t, env, "add", "code.txt")

	result, err := (&gitRestoreTool{}).Run(context.Background(), env, map[string]any{
		"paths": []any{"code.txt"}, "staged": true,
	})
	if err != nil || result.IsError {
		t.Fatalf("unstage failed: %+v err=%v", result, err)
	}

	// Unstaging must keep the edit, which is the difference from discarding.
	content, _ := os.ReadFile(filepath.Join(env.Workspace, "code.txt"))
	if string(content) != "edited\n" {
		t.Errorf("unstaging lost the work: %q", content)
	}
	status, _ := (&gitStatusTool{}).Run(context.Background(), env, map[string]any{})
	if !strings.Contains(status.Output, "code.txt") {
		t.Errorf("the change should still be present as unstaged: %q", status.Output)
	}
}

// TestGitRestoreRefusesAWholeTreeRevert keeps one mistake out of reach: with no
// paths, git would discard every uncommitted change in the repository.
func TestGitRestoreRefusesAWholeTreeRevert(t *testing.T) {
	env := gitEnv(t)
	result, _ := (&gitRestoreTool{}).Run(context.Background(), env, map[string]any{})
	if !result.IsError {
		t.Fatalf("a pathless restore must be refused")
	}
	if !strings.Contains(result.Output, "at least one path") {
		t.Errorf("the refusal should explain itself: %q", result.Output)
	}
}

func TestGitBranchListsSwitchesAndCreates(t *testing.T) {
	env := gitEnv(t)
	git(t, env, "commit", "--allow-empty", "-m", "init")

	listed, err := (&gitBranchTool{}).Run(context.Background(), env, map[string]any{})
	if err != nil || listed.IsError {
		t.Fatalf("list failed: %+v err=%v", listed, err)
	}
	if !strings.Contains(listed.Output, "main") {
		t.Errorf("the list should include main: %q", listed.Output)
	}

	created, err := (&gitBranchTool{}).Run(context.Background(), env, map[string]any{
		"name": "feature/x", "create": true,
	})
	if err != nil || created.IsError {
		t.Fatalf("create failed: %+v err=%v", created, err)
	}
	status, _ := (&gitStatusTool{}).Run(context.Background(), env, map[string]any{})
	if !strings.Contains(status.Output, "feature/x") {
		t.Errorf("the branch was not switched to: %q", status.Output)
	}
}

func TestGitBlameShowsAuthorAndLines(t *testing.T) {
	env := gitEnv(t)
	writeTestFile(t, env, "file.txt", "line 1\nline 2\nline 3\n")
	git(t, env, "add", "file.txt")
	git(t, env, "commit", "-m", "first commit")

	result, err := (&gitBlameTool{}).Run(context.Background(), env, map[string]any{
		"path": "file.txt",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("blame failed: %s", result.Output)
	}
	if !strings.Contains(result.Output, "Termixgo Test") {
		t.Errorf("blame should contain author name: %s", result.Output)
	}
	if !strings.Contains(result.Output, "line 1") || !strings.Contains(result.Output, "line 3") {
		t.Errorf("blame should contain file lines: %s", result.Output)
	}

	// Test line range
	rangeResult, err := (&gitBlameTool{}).Run(context.Background(), env, map[string]any{
		"path":       "file.txt",
		"start_line": 2,
		"end_line":   2,
	})
	if err != nil {
		t.Fatalf("Run range: %v", err)
	}
	if rangeResult.IsError {
		t.Fatalf("blame range failed: %s", rangeResult.Output)
	}
	if !strings.Contains(rangeResult.Output, "line 2") {
		t.Errorf("blame range should contain line 2: %s", rangeResult.Output)
	}
	if strings.Contains(rangeResult.Output, "line 1") || strings.Contains(rangeResult.Output, "line 3") {
		t.Errorf("blame range should not contain other lines: %s", rangeResult.Output)
	}
}

func TestGitToolsAreRegisteredWithConsistentMetadata(t *testing.T) {
	registry := DefaultRegistry()
	mutating := map[string]bool{
		"git_add": true, "git_commit": true, "git_branch": true, "git_restore": true,
	}
	readOnly := []string{"git_status", "git_diff", "git_log", "git_show", "git_blame"}

	for _, name := range readOnly {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if tool.Mutating() {
			t.Errorf("%s must not be marked mutating", name)
		}
	}
	for name, wantMutating := range mutating {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if tool.Mutating() != wantMutating {
			t.Errorf("%s Mutating = %v, want %v", name, tool.Mutating(), wantMutating)
		}
	}
}

func TestBackgroundToolsAreRegistered(t *testing.T) {
	registry := DefaultRegistry()
	for _, name := range []string{"run_background", "run_logs", "run_wait", "run_list", "run_kill"} {
		if _, ok := registry.Lookup(name); !ok {
			t.Errorf("%s is not registered", name)
		}
	}
	// The aliases matter: a model trained on other agents will reach for
	// these names.
	for _, alias := range []string{"bash_background", "bash_logs", "bash_kill", "bash_list"} {
		if _, ok := registry.Lookup(alias); !ok {
			t.Errorf("alias %s does not resolve", alias)
		}
	}
}

func TestGitStashLifecycle(t *testing.T) {
	env := gitEnv(t)
	writeTestFile(t, env, "file.txt", "initial\n")
	git(t, env, "add", "file.txt")
	git(t, env, "commit", "-m", "initial commit")

	// Modify file
	writeTestFile(t, env, "file.txt", "modified\n")

	// Stash changes
	stashRes, err := (&gitStashTool{}).Run(context.Background(), env, map[string]any{"message": "work in progress"})
	if err != nil || stashRes.IsError {
		t.Fatalf("git_stash failed: %v, out: %s", err, stashRes.Output)
	}

	// Verify working tree is clean
	data, err := os.ReadFile(filepath.Join(env.Workspace, "file.txt"))
	if err != nil || string(data) != "initial\n" {
		t.Fatalf("file should be restored to initial state after stash, got %q", string(data))
	}

	// List stash
	listRes, err := (&gitStashListTool{}).Run(context.Background(), env, map[string]any{})
	if err != nil || listRes.IsError || !strings.Contains(listRes.Output, "work in progress") {
		t.Fatalf("git_stash_list should show our stash, got: %s", listRes.Output)
	}

	// Pop stash
	popRes, err := (&gitStashPopTool{}).Run(context.Background(), env, map[string]any{})
	if err != nil || popRes.IsError {
		t.Fatalf("git_stash_pop failed: %v, out: %s", err, popRes.Output)
	}

	// Verify modified content restored
	data, err = os.ReadFile(filepath.Join(env.Workspace, "file.txt"))
	if err != nil || string(data) != "modified\n" {
		t.Fatalf("file should have modified content after stash pop, got %q", string(data))
	}
}

func TestGitConflictsDetection(t *testing.T) {
	env := gitEnv(t)
	conflictContent := "header\n<<<<<<< HEAD\ncode from main\n=======\ncode from feature\n>>>>>>> feature\nfooter\n"
	writeTestFile(t, env, "conflict.go", conflictContent)

	res, err := (&gitConflictsTool{}).Run(context.Background(), env, map[string]any{"path": "conflict.go"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(res.Output, "Merge Conflicts Detected") {
		t.Fatalf("expected conflicts detected, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "code from main") || !strings.Contains(res.Output, "code from feature") {
		t.Fatalf("expected conflicted code hunks, got: %s", res.Output)
	}
}
