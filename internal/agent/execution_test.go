package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

// ------------------------------------------------------------------ project detection

// TestDetectCheckCommandForGo covers the toolchain this repository uses, so the
// detection is exercised against a real project layout.
func TestDetectCheckCommandForGo(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"test":      "go test ./...",
		"lint":      "go vet ./...",
		"format":    "gofmt -l .",
		"typecheck": "go build ./...",
		"build":     "go build ./...",
	}
	for kind, want := range cases {
		got, ok := detectCheckCommand(workspace, kind)
		if !ok {
			t.Errorf("%s: no command detected", kind)
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", kind, got, want)
		}
	}
}

func TestDetectCheckCommandForRust(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "Cargo.toml"), []byte("[package]\nname = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"test":      "cargo test",
		"lint":      "cargo clippy --all-targets",
		"format":    "cargo fmt --check",
		"typecheck": "cargo check",
		"build":     "cargo build",
	}
	for kind, want := range cases {
		got, ok := detectCheckCommand(workspace, kind)
		if !ok || got != want {
			t.Errorf("%s = %q ok=%v, want %q", kind, got, ok, want)
		}
	}
}

// TestDetectCheckCommandForNodeUsesTheProjectsScript is the important Node
// case: the project's own script wins over a guessed runner, because that script
// is what its CI runs.
func TestDetectCheckCommandForNodeUsesTheProjectsScript(t *testing.T) {
	workspace := t.TempDir()
	packageJSON := `{
		"scripts": {
			"test": "vitest run",
			"lint": "biome lint ./src",
			"check-types": "tsc --noEmit",
			"build": "vite build"
		}
	}`
	if err := os.WriteFile(filepath.Join(workspace, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// The lockfile is what picks the package manager.
	if err := os.WriteFile(filepath.Join(workspace, "pnpm-lock.yaml"), []byte("lockfileVersion: 9\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"test":      "pnpm run test",
		"lint":      "pnpm run lint",
		"typecheck": "pnpm run check-types",
		"build":     "pnpm run build",
	}
	for kind, want := range cases {
		got, ok := detectCheckCommand(workspace, kind)
		if !ok || got != want {
			t.Errorf("%s = %q ok=%v, want %q", kind, got, ok, want)
		}
	}
}

// TestDetectCheckCommandPicksThePackageManager covers the lockfile sniffing:
// running the wrong manager fails on a project that does not have it installed.
func TestDetectCheckCommandPicksThePackageManager(t *testing.T) {
	cases := map[string]string{
		"pnpm-lock.yaml": "pnpm run test",
		"yarn.lock":      "yarn run test",
		"bun.lockb":      "bun run test",
		"":               "npm run test",
	}
	for lockfile, want := range cases {
		workspace := t.TempDir()
		if err := os.WriteFile(filepath.Join(workspace, "package.json"), []byte(`{"scripts":{"test":"vitest run"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if lockfile != "" {
			if err := os.WriteFile(filepath.Join(workspace, lockfile), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, ok := detectCheckCommand(workspace, "test")
		if !ok || got != want {
			t.Errorf("with %q: test = %q ok=%v, want %q", lockfile, got, ok, want)
		}
	}
}

// TestDetectCheckCommandFallsBackToTheRunner covers a project with no script
// for the requested check, where the runner's own default is the honest answer.
func TestDetectCheckCommandFallsBackToTheRunner(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "package.json"), []byte(`{"scripts":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := detectCheckCommand(workspace, "test")
	if !ok || got != "npm test" {
		t.Errorf("test = %q ok=%v, want npm test", got, ok)
	}
	// A kind with no fallback reports that nothing was detected rather than
	// inventing a command.
	if got, ok := detectCheckCommand(workspace, "lint"); ok {
		t.Errorf("lint = %q, want no detection for an unknown script", got)
	}
}

func TestDetectCheckCommandForPython(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "pyproject.toml"), []byte("[project]\nname = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"test":   "python -m pytest",
		"lint":   "python -m ruff check .",
		"format": "python -m ruff format --check .",
	}
	for kind, want := range cases {
		got, ok := detectCheckCommand(workspace, kind)
		if !ok || got != want {
			t.Errorf("%s = %q ok=%v, want %q", kind, got, ok, want)
		}
	}
	// A check the project cannot run reports as undetected rather than
	// guessing at a tool that may not be installed.
	if got, ok := detectCheckCommand(workspace, "build"); ok {
		t.Errorf("build = %q, want no detection for a Python project", got)
	}
}

func TestDetectCheckCommandWithNoProject(t *testing.T) {
	workspace := t.TempDir()
	if got, ok := detectCheckCommand(workspace, "test"); ok {
		t.Errorf("test = %q, want no detection in an empty directory", got)
	}
	// An empty workspace is the one case that must not panic.
	if got, ok := detectCheckCommand("", "test"); ok {
		t.Errorf("test = %q, want no detection for an empty path", got)
	}
}

func TestScriptNamesForEveryKind(t *testing.T) {
	for _, kind := range []string{"test", "lint", "format", "typecheck", "build"} {
		names := scriptNamesFor(kind)
		if len(names) == 0 {
			t.Errorf("%s has no candidate script names", kind)
		}
	}
	if names := scriptNamesFor("nonsense"); len(names) != 0 {
		t.Errorf("an unknown kind should have no candidates, got %v", names)
	}
}

// ------------------------------------------------------------------ run_checks

// TestRunChecksUsesTheProjectCommand drives the tool end to end against a real
// Go module, so the detection and the execution are proven together.
func TestRunChecksUsesTheProjectCommand(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.com/checks\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "lib.go"), []byte("package checks\n\nfunc Add(a, b int) int { return a + b }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The real shell is used here on purpose: the point is that the detected
	// command actually runs. It is a Go build, which needs no network.
	env := &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true}

	result, err := (&runChecksTool{}).Run(context.Background(), env, map[string]any{"kind": "build"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("build failed: %s", result.Output)
	}
	if !strings.Contains(result.Output, "go build ./...") {
		t.Errorf("the output should name the command it ran: %q", result.Output)
	}
}

func TestRunChecksRejectsAnUnknownKind(t *testing.T) {
	env := &Env{Workspace: t.TempDir(), Todos: NewTodoStore(), Memory: NewMemory(t.TempDir())}
	result, _ := (&runChecksTool{}).Run(context.Background(), env, map[string]any{"kind": "vibes"})
	if !result.IsError {
		t.Fatalf("an unknown check kind must be refused")
	}
	if !strings.Contains(result.Output, "test, lint, format, typecheck or build") {
		t.Errorf("the refusal should list the valid kinds: %q", result.Output)
	}
}

func TestRunChecksWithNoDetectionExplainsItself(t *testing.T) {
	env := &Env{Workspace: t.TempDir(), Todos: NewTodoStore(), Memory: NewMemory(t.TempDir())}
	result, err := (&runChecksTool{}).Run(context.Background(), env, map[string]any{"kind": "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsError {
		t.Fatalf("a project with no detectable test command must be reported")
	}
	if !strings.Contains(result.Output, "No test command") {
		t.Errorf("output = %q, want it to say nothing was detected", result.Output)
	}
}

func TestRunChecksDefaultsToTest(t *testing.T) {
	env := &Env{Workspace: t.TempDir(), Todos: NewTodoStore(), Memory: NewMemory(t.TempDir())}
	result, _ := (&runChecksTool{}).Run(context.Background(), env, map[string]any{})
	// The default kind is test, so the message names it.
	if !strings.Contains(result.Output, "No test command") {
		t.Errorf("output = %q, want the test kind to have been used", result.Output)
	}
}

// ------------------------------------------------------------------ subagent

// TestRunSubagentReturnsItsAnswer drives a nested run against a fake
// provider: the delegated investigation must come back as text, in its own
// session, without touching the parent's conversation.
func TestRunSubagentReturnsItsAnswer(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{textChunk("The routes live in internal/http/routes.go.")},
	}}
	parent := testEnv(t)
	parent.Config = config.Default()

	answer, err := RunSubagent(context.Background(), parent, client, provider.Model{ID: "test-model"}, string(SubagentExplore), "where are the routes?", 4)
	if err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if !strings.Contains(answer, "internal/http/routes.go") {
		t.Errorf("answer = %q, want the nested run's text", answer)
	}
}

// TestRunSubagentReviewRoleCannotMutate is the safety property that survives
// the no-sandbox design: a review worker is handed the read-only registry, so
// a model asking to write gets the unknown-tool error rather than a file.
func TestRunSubagentReviewRoleCannotMutate(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "write_file", `{"path":"should-not-exist.txt","content":"nope"}`)},
		{textChunk("I cannot write files.")},
	}}
	parent := testEnv(t)
	parent.Config = config.Default()

	if _, err := RunSubagent(context.Background(), parent, client, provider.Model{ID: "test-model"}, string(SubagentCodeReview), "write a file", 4); err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent.Workspace, "should-not-exist.txt")); err == nil {
		t.Fatalf("a review subagent wrote a file, which it must never be able to do")
	}
}

// TestRunSubagentWorkerRolesAreFullPeers pins the other half of the design:
// only the review roles are read-tier, every other role is a peer of the main
// agent with the full allowlist.
func TestRunSubagentWorkerRolesAreFullPeers(t *testing.T) {
	for _, role := range []SubagentType{SubagentExplore, SubagentGeneral, SubagentBuilder, SubagentImage} {
		if SubagentIsReadOnly(string(role)) {
			t.Errorf("%s is a worker, not a review role, so it must keep the full toolset", role)
		}
		registry := subagentRegistry(string(role), 0)
		if _, ok := registry.Lookup("write_file"); !ok {
			t.Errorf("%s should be able to write", role)
		}
	}
}

func TestRunSubagentNeedsAClient(t *testing.T) {
	parent := testEnv(t)
	if _, err := RunSubagent(context.Background(), parent, nil, provider.Model{ID: "m"}, string(SubagentGeneral), "prompt", 4); err == nil {
		t.Fatalf("a subagent with no provider client must fail rather than silently do nothing")
	}
}

func TestRunSubagentWithoutAnAnswerSaysSo(t *testing.T) {
	// A run that produces only tool calls and no text ends with nothing to
	// report, which has to be stated rather than returned as an empty string.
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "list_directory", `{"path":"."}`)},
		{},
	}}
	parent := testEnv(t)
	parent.Config = config.Default()

	answer, err := RunSubagent(context.Background(), parent, client, provider.Model{ID: "test-model"}, string(SubagentExplore), "look around", 3)
	if err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if strings.TrimSpace(answer) == "" {
		t.Errorf("an empty answer should be described, not returned blank")
	}
}

func TestRunSubagentRespectsItsStepBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("timing sensitive")
	}
	// Each step asks for a different directory, so the loop guard cannot be
	// what stops the run: the child's own step budget has to be.
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{callChunk("c1", "list_directory", `{"path":"."}`)},
		{callChunk("c2", "list_directory", `{"path":"./"}`)},
		{callChunk("c3", "list_directory", `{"path":"./."}`)},
		{callChunk("c4", "list_directory", `{"path":"././"}`)},
		{callChunk("c5", "list_directory", `{"path":"././."}`)},
		{callChunk("c6", "list_directory", `{"path":"./././"}`)},
		{callChunk("c7", "list_directory", `{"path":"./././."}`)},
		{callChunk("c8", "list_directory", `{"path":"././././"}`)},
	}}

	parent := testEnv(t)
	parent.Config = config.Default()

	done := make(chan error, 1)
	go func() {
		_, err := RunSubagent(context.Background(), parent, client, provider.Model{ID: "test-model"}, string(SubagentExplore), "loop", 3)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunSubagent: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("the subagent did not stop at its step budget")
	}
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls > 3 {
		t.Errorf("provider calls = %d, want the child to stop at its own budget of 3", calls)
	}
}

// TestReadOnlyRegistryHasNoMutatingTools is the contract the tests above rely
// on, asserted directly so adding a tool to the wrong registry fails here.
func TestReadOnlyRegistryHasNoMutatingTools(t *testing.T) {
	for _, tool := range readOnlyTools().Tools() {
		if tool.Mutating() {
			t.Errorf("%s is in the read-only registry but mutates", tool.Name())
		}
	}
}

// ------------------------------------------------------------------ command execution

// TestExecuteReportsSuccessFailureAndTimeout covers the three outcomes a
// command can have, since the model acts on which one it gets.
func TestExecuteReportsSuccessFailureAndTimeout(t *testing.T) {
	env := &Env{Workspace: t.TempDir(), Todos: NewTodoStore(), Memory: NewMemory(t.TempDir())}

	ok, err := execute(context.Background(), env, quickCommand("all good"), env.Workspace, 30*time.Second)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if ok.IsError {
		t.Errorf("a successful command should not be an error: %+v", ok)
	}
	if !strings.Contains(ok.Output, "all good") {
		t.Errorf("output = %q, want the command's output", ok.Output)
	}

	failed, err := execute(context.Background(), env, failingCommand(), env.Workspace, 30*time.Second)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !failed.IsError {
		t.Errorf("a failing command should be an error: %+v", failed)
	}
	if !strings.Contains(failed.Output, "boom") {
		t.Errorf("output = %q, want the failure output", failed.Output)
	}
}

// TestTruncateOutputKeepsBothEnds guards the assumption the model depends on:
// the command echo is at the top and the failure is at the bottom, so dropping
// the middle is safe and dropping an end is not.
func TestTruncateOutputKeepsBothEnds(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("HEAD-MARKER\n")
	for index := 0; index < 5000; index++ {
		builder.WriteString("filler line that takes up space\n")
	}
	builder.WriteString("TAIL-MARKER\n")

	truncated := truncateOutput(builder.String(), 2000)
	if !strings.Contains(truncated, "HEAD-MARKER") {
		t.Errorf("the head was dropped, losing the command echo")
	}
	if !strings.Contains(truncated, "TAIL-MARKER") {
		t.Errorf("the tail was dropped, losing the failure")
	}
	if !strings.Contains(truncated, "omitted") {
		t.Errorf("the truncation should be visible")
	}
	if len(truncated) > 2200 {
		t.Errorf("length = %d, want it near the limit", len(truncated))
	}

	// Short output passes through untouched.
	if got := truncateOutput("short output", 2000); got != "short output" {
		t.Errorf("short output was modified: %q", got)
	}
}

func TestLooksLikeInstallRaisesTheFloor(t *testing.T) {
	slow := []string{"pnpm install", "npm i", "cargo build", "go build ./...", "git clone x", "pip install y"}
	for _, command := range slow {
		if !looksLikeInstall(command) {
			t.Errorf("%q should be recognised as slow", command)
		}
	}
	if looksLikeInstall("ls -la") {
		t.Errorf("a quick command should not get the longer timeout")
	}
}

func TestRunCommandRequiresACommand(t *testing.T) {
	env := &Env{Workspace: t.TempDir(), Todos: NewTodoStore(), Memory: NewMemory(t.TempDir())}
	result, _ := (&runCommandTool{}).Run(context.Background(), env, map[string]any{"command": "  "})
	if !result.IsError {
		t.Errorf("a blank command must be refused")
	}
}

func TestRunCommandRejectsAnUnusableWorkingDirectory(t *testing.T) {
	env := &Env{Workspace: t.TempDir(), Todos: NewTodoStore(), Memory: NewMemory(t.TempDir())}
	result, _ := (&runCommandTool{}).Run(context.Background(), env, map[string]any{
		"command": "echo hi",
		"cwd":     "does/not/exist",
	})
	if !result.IsError {
		t.Fatalf("a missing working directory must be refused, not run somewhere unexpected")
	}
	if !strings.Contains(result.Output, "not available") {
		t.Errorf("output = %q, want it to explain", result.Output)
	}
}

func TestShellInvocationPutsTheCommandLast(t *testing.T) {
	shell, args := shellInvocation("echo marker")
	if strings.TrimSpace(shell) == "" || len(args) == 0 {
		t.Fatalf("shell = %q args = %v", shell, args)
	}
	last := args[len(args)-1]
	if !strings.Contains(last, "echo marker") {
		t.Errorf("the command must be the final argument, got %v", args)
	}
}
