package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

// tree writes a small workspace with a directory, a nested Go file, a JSON
// file and a generated tree that the search tools must skip.
func tree(t *testing.T) *Env {
	t.Helper()
	env := testEnv(t)
	files := map[string]string{
		"main.go":                  "package main\n",
		"README.md":                "# readme\n",
		"data/config.json":         "{}\n",
		"data/deep/nested/loop.go": "package deep\n",
		"node_modules/junk.go":     "package junk\n",
		".git/hooks/pre-commit":    "#!/bin/sh\n",
	}
	for name, content := range files {
		full := filepath.Join(env.Workspace, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return env
}

func TestGlobRequiresAPattern(t *testing.T) {
	env := tree(t)
	for _, args := range []map[string]any{nil, {}, {"pattern": "   "}} {
		result, err := (&globTool{}).Run(context.Background(), env, args)
		if err != nil || !result.IsError {
			t.Fatalf("args %v: err=%v result=%+v", args, err, result)
		}
	}
}

// TestGlobFindsFilesAndSkipsGeneratedTrees is the contract the model relies on:
// a pattern is matched against the workspace-relative path, and the trees that
// are never interesting do not come back.
func TestGlobFindsFilesAndSkipsGeneratedTrees(t *testing.T) {
	env := tree(t)

	result, err := (&globTool{}).Run(context.Background(), env, map[string]any{"pattern": "**/*.go"})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if !strings.HasPrefix(result.Output, "2 file(s)") {
		t.Errorf("output = %q, want two files", result.Output)
	}
	for _, want := range []string{"main.go", "data/deep/nested/loop.go"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output is missing %q:\n%s", want, result.Output)
		}
	}
	// Skipping node_modules and .git is what keeps a search usable in a real
	// checkout, where those trees dwarf the source.
	for _, unwanted := range []string{"node_modules", ".git"} {
		if strings.Contains(result.Output, unwanted) {
			t.Errorf("output should not include %q:\n%s", unwanted, result.Output)
		}
	}
	// Results are workspace-relative and slash-separated on every platform.
	if strings.Contains(result.Output, env.Workspace) {
		t.Errorf("output should be relative:\n%s", result.Output)
	}
}

func TestGlobHonoursSubdirectoryAndAbsoluteRoot(t *testing.T) {
	env := tree(t)

	scoped, err := (&globTool{}).Run(context.Background(), env, map[string]any{
		"pattern": "*.go", "path": "data",
	})
	if err != nil || scoped.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, scoped)
	}
	if !strings.Contains(scoped.Output, "data/deep/nested/loop.go") {
		t.Errorf("a scoped search should find the nested file:\n%s", scoped.Output)
	}
	if strings.Contains(scoped.Output, "main.go") {
		t.Errorf("a scoped search should not escape its root:\n%s", scoped.Output)
	}

	// An absolute path is accepted, because the model sometimes builds one.
	absolute, err := (&globTool{}).Run(context.Background(), env, map[string]any{
		"pattern": "*.go", "path": filepath.Join(env.Workspace, "data"),
	})
	if err != nil || absolute.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, absolute)
	}
	if !strings.Contains(absolute.Output, "loop.go") {
		t.Errorf("output = %q, want the nested file", absolute.Output)
	}
}

func TestGlobSaysWhenNothingMatches(t *testing.T) {
	env := tree(t)
	result, err := (&globTool{}).Run(context.Background(), env, map[string]any{"pattern": "**/*.rs"})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if result.Output != "No files matched." {
		t.Errorf("output = %q, want the no-match notice", result.Output)
	}
}

// TestGlobStopsAtTheLimit keeps one query from returning an unbounded list.
// The model has to be told the list was cut, or it will report a partial
// answer as a complete one.
func TestGlobStopsAtTheLimit(t *testing.T) {
	env := testEnv(t)
	for index := 0; index < 12; index++ {
		name := filepath.Join(env.Workspace, fmt.Sprintf("file-%02d.go", index))
		if err := os.WriteFile(name, []byte("package p\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	result, err := (&globTool{}).Run(context.Background(), env, map[string]any{
		"pattern": "*.go", "max_results": 5,
	})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if !strings.HasPrefix(result.Output, "5 file(s) (limit reached)") {
		t.Errorf("output = %q, want the limit reported", result.Output)
	}
	if lines := strings.Count(result.Output, "\n"); lines != 5 {
		t.Errorf("output has %d result lines, want 5:\n%s", lines, result.Output)
	}
}

// TestGlobStopsOnACancelledContext is what makes Esc responsive during a search
// over a large tree.
func TestGlobStopsOnACancelledContext(t *testing.T) {
	env := tree(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := (&globTool{}).Run(ctx, env, map[string]any{"pattern": "**/*.go"})
	if err != nil {
		t.Fatalf("a cancelled search must be a result, not a Go error: %v", err)
	}
	// A walk that stops early leaves a partial or empty answer; either way it
	// must not be reported as a failure the model should retry.
	if result.IsError {
		t.Errorf("a cancelled search should not be an error, got %q", result.Output)
	}
}

func TestGlobReportsAnUnusableRoot(t *testing.T) {
	env := tree(t)
	result, err := (&globTool{}).Run(context.Background(), env, map[string]any{
		"pattern": "*.go", "path": filepath.Join(env.Workspace, "missing-dir"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// A missing root produces no matches rather than a hard failure: the model
	// can then search elsewhere instead of being blocked.
	if result.IsError && !strings.Contains(result.Output, "glob failed") {
		t.Errorf("output = %q", result.Output)
	}
}

func TestRelativeSlashWithoutAWorkspace(t *testing.T) {
	path := filepath.Join("a", "b", "c.go")
	if got := relativeSlash(&Env{}, path); got != "a/b/c.go" {
		t.Errorf("relativeSlash = %q, want a slash path", got)
	}
	env := &Env{Workspace: t.TempDir()}
	inside := filepath.Join(env.Workspace, "x", "y.go")
	if got := relativeSlash(env, inside); got != "x/y.go" {
		t.Errorf("relativeSlash = %q, want x/y.go", got)
	}
}

// ------------------------------------------------- directory listing

func TestSortEntriesPutsDirectoriesFirstThenNames(t *testing.T) {
	env := testEnv(t)
	for _, name := range []string{"zebra.txt", "Apple.txt", "beta"} {
		full := filepath.Join(env.Workspace, name)
		if name == "beta" {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			continue
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(env.Workspace, "Alpha"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	entries, err := os.ReadDir(env.Workspace)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	sortEntries(entries)

	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := []string{"Alpha", "beta", "Apple.txt", "zebra.txt"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v (directories first, then case-insensitive)", names, want)
	}
}

func TestIsTextFileRejectsBinariesAndAcceptsSource(t *testing.T) {
	for _, name := range []string{
		"image.PNG", "photo.jpeg", "archive.zip", "lib.dll", "app.exe",
		"font.woff2", "doc.pdf", "bundle.tar.gz", "model.bin",
	} {
		if isTextFile(name) {
			t.Errorf("%s is a binary and must be refused", name)
		}
	}
	for _, name := range []string{"main.go", "notes.md", "data.json", "run.sh", "Makefile", "no-extension"} {
		if !isTextFile(name) {
			t.Errorf("%s is text and must be allowed", name)
		}
	}
}

// ------------------------------------------------- edit diagnostics

// TestDiagnoseNamesTheDifferences is what turns a failed edit into a usable
// retry. Each branch is a mistake a model actually makes.
func TestDiagnoseNamesTheDifferences(t *testing.T) {
	text := "func main() {\n\tprintln(\"hi\")\n}\n"

	cases := []struct {
		name   string
		needle string
		want   string
	}{
		{"trailing whitespace", "func main() {   ", "whitespace"},
		{"different case", "FUNC MAIN() {", "Case differs"},
		{"collapsed inner whitespace", "func    main() {", "Whitespace inside"},
		{"unrelated text", "package other", "verbatim"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := diagnose(text, testCase.needle)
			if !strings.Contains(strings.ToLower(got), strings.ToLower(testCase.want)) {
				t.Errorf("diagnose = %q, want it to mention %q", got, testCase.want)
			}
		})
	}
}

func TestMatchLinesReportsAtMostFiveStarts(t *testing.T) {
	text := strings.Repeat("needle\n", 9)
	lines := matchLines(text, "needle")
	if len(lines) != 5 {
		t.Fatalf("matchLines = %v, want five", lines)
	}
	if lines[0] != "1" || lines[4] != "5" {
		t.Errorf("lines = %v, want the first five positions", lines)
	}
	if got := matchLines("nothing here", "needle"); len(got) != 0 {
		t.Errorf("matchLines = %v, want none", got)
	}
}

// ------------------------------------------------- arguments

func TestDecodeToolArgumentsAcceptsTheShapesModelsSend(t *testing.T) {
	empty, err := decodeToolArguments("   ")
	if err != nil || len(empty) != 0 {
		t.Errorf("a blank argument string = %v, %v; want an empty object", empty, err)
	}
	nulled, err := decodeToolArguments("null")
	if err != nil || len(nulled) != 0 {
		t.Errorf("a null body = %v, %v; want an empty object", nulled, err)
	}
	decoded, err := decodeToolArguments(`{"path":"a.go","n":3}`)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["path"] != "a.go" {
		t.Errorf("decoded = %v", decoded)
	}
	// A truncated stream is the common failure, and the message has to say so
	// rather than reporting a Go decode error.
	if _, err := decodeToolArguments(`{"path":`); err == nil {
		t.Errorf("truncated JSON must be rejected")
	}
	if _, err := decodeToolArguments(`["not","an","object"]`); err == nil {
		t.Errorf("an array is not an argument object")
	}
}

// ------------------------------------------------- todos

func TestParseTodosAcceptsAJSONString(t *testing.T) {
	// A model sometimes sends the array as a JSON string rather than a list.
	items, err := parseTodos(`[{"id":"1","title":"first","status":"pending"}]`)
	if err != nil {
		t.Fatalf("parseTodos: %v", err)
	}
	if len(items) != 1 || items[0].Title != "first" {
		t.Fatalf("items = %+v", items)
	}
	if _, err := parseTodos("{not json"); err == nil {
		t.Errorf("a malformed string must be rejected")
	}
}

func TestParseTodosAcceptsTheAliasedFieldNames(t *testing.T) {
	items, err := parseTodos([]any{
		map[string]any{"id": "1", "description": "from description", "status": "pending"},
		map[string]any{"text": "from text", "status": "completed", "parent": "1"},
	})
	if err != nil {
		t.Fatalf("parseTodos: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Title != "from description" || items[1].Title != "from text" {
		t.Errorf("aliases were not read: %+v", items)
	}
	if items[1].Parent != "1" {
		t.Errorf("parent = %q, want 1", items[1].Parent)
	}
}

func TestParseTodosRejectsUnusableInput(t *testing.T) {
	if _, err := parseTodos(nil); err == nil {
		t.Errorf("a missing plan must be refused")
	}
	if _, err := parseTodos("just a sentence"); err == nil {
		t.Errorf("a plain sentence must be refused")
	}
	if _, err := parseTodos(map[string]any{"todos": []any{}}); err == nil {
		t.Errorf("an object is not a plan")
	}
	if _, err := parseTodos([]any{"a string item"}); err == nil {
		t.Errorf("an item that is not an object must be refused")
	}
}

// ------------------------------------------------- the prompt

func TestRunnerSystemPrefersAnExplicitPrompt(t *testing.T) {
	env := testEnv(t)
	runner := &Runner{Env: env, Model: "test-model"}

	built := runner.system(NewSession(env.Workspace, "test-model"))
	if !strings.Contains(built, "Termixgo") {
		t.Errorf("the built prompt should name the agent:\n%s", built)
	}

	runner.System = "You are a test double."
	if got := runner.system(NewSession(env.Workspace, "")); got != "You are a test double." {
		t.Errorf("an explicit prompt must win, got %q", got)
	}
	// A whitespace-only override is not an override.
	runner.System = "   "
	if got := runner.system(NewSession(env.Workspace, "")); got == "   " {
		t.Errorf("a blank override must fall back to the built prompt")
	}
}

func TestTrustLabelIsShortAndExplicit(t *testing.T) {
	if got := TrustLabel(true); got != "trusted" {
		t.Errorf("TrustLabel(true) = %q", got)
	}
	if got := TrustLabel(false); got != "untrusted" {
		t.Errorf("TrustLabel(false) = %q", got)
	}
}

// ------------------------------------------------- memory storage

func TestMemoryPathsFollowTheScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	workspace := t.TempDir()
	memory := NewMemory(workspace)

	if got := memory.projectPath(); got != filepath.Join(workspace, ".termixgo", "memory.md") {
		t.Errorf("projectPath = %q", got)
	}
	if got := memory.globalPath(); got != filepath.Join(home, "memory.md") {
		t.Errorf("globalPath = %q", got)
	}
	// With no workspace there is nowhere to write project memory, which is the
	// state a one-shot run with no folder would be in.
	if got := NewMemory("  ").projectPath(); got != "" {
		t.Errorf("projectPath = %q, want empty", got)
	}
}

func TestRememberRejectsWhatItCannotStore(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	memory := NewMemory(t.TempDir())

	if err := memory.Remember("   ", "project"); err == nil {
		t.Errorf("an empty fact must be refused")
	}
	// A workspace-less memory has no project file, so the scope has to be
	// refused rather than silently writing nowhere.
	if err := NewMemory("").Remember("a fact", "project"); err == nil {
		t.Errorf("a memory with no location must be refused")
	}
}

func TestRememberCapsTheStoredFacts(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	memory := NewMemory(t.TempDir())

	// The same fact twice is stored once, which is what keeps the block from
	// filling with a model repeating itself.
	if err := memory.Remember("run make check", "project"); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if err := memory.Remember("  RUN MAKE CHECK  ", "project"); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	project, _ := memory.Read()
	if len(project) != 1 {
		t.Fatalf("facts = %v, want one", project)
	}

	// The count is capped, and the oldest fall off first.
	for index := 0; index < maxMemoryFacts+10; index++ {
		if err := memory.Remember(fmt.Sprintf("fact number %d", index), "project"); err != nil {
			t.Fatalf("Remember %d: %v", index, err)
		}
	}
	project, _ = memory.Read()
	if len(project) != maxMemoryFacts {
		t.Errorf("facts = %d, want the cap of %d", len(project), maxMemoryFacts)
	}
	if project[len(project)-1] != fmt.Sprintf("fact number %d", maxMemoryFacts+9) {
		t.Errorf("the newest fact should be last, got %q", project[len(project)-1])
	}

	// A single fact is clipped so one enormous line cannot fill the block.
	long := strings.Repeat("y", maxFactChars+100)
	if err := memory.Remember(long, "project"); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	project, _ = memory.Read()
	if got := len(project[len(project)-1]); got != maxFactChars {
		t.Errorf("clipped fact = %d chars, want %d", got, maxFactChars)
	}
}

func TestMemoryPromptBlockRendersBothScopes(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	memory := NewMemory(t.TempDir())

	if block := memory.PromptBlock(); block != "" {
		t.Errorf("an empty memory should render nothing, got %q", block)
	}
	if err := memory.Remember("this repo uses tabs", "global"); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if err := memory.Remember("no em-dashes here", "project"); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	block := memory.PromptBlock()
	for _, want := range []string{"## LEARNED MEMORY", "GLOBAL CONVENTIONS", "this repo uses tabs", "PROJECT CONVENTIONS", "no em-dashes here"} {
		if !strings.Contains(block, want) {
			t.Errorf("the block is missing %q:\n%s", want, block)
		}
	}
}

// ------------------------------------------------- git plumbing

func TestRunGitRefusesOutsideARepository(t *testing.T) {
	env := testEnv(t)
	if _, err := runGit(context.Background(), env, "status"); err == nil {
		t.Fatalf("git outside a repository must fail")
	} else if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err = %v, want it to name the problem", err)
	}
}

// TestRunGitReportsAFailingCommandAsOutput is what lets the model see git's own
// message instead of a bare exit status.
func TestRunGitReportsAFailingCommandAsOutput(t *testing.T) {
	env := initGitRepo(t)

	output, err := runGit(context.Background(), env, "show", "does-not-exist-ref")
	if err == nil {
		t.Fatalf("an unknown ref must fail")
	}
	if !strings.Contains(err.Error(), "git show failed") {
		t.Errorf("err = %v, want it to name the command", err)
	}
	if !strings.Contains(output, "does-not-exist-ref") {
		t.Errorf("output = %q, want git's own message", output)
	}
}

func TestRunGitCapsALongOutput(t *testing.T) {
	env := initGitRepo(t)
	// A log with a lot of context is the realistic way to exceed the cap.
	for index := 0; index < 3; index++ {
		name := filepath.Join(env.Workspace, fmt.Sprintf("filler-%d.txt", index))
		if err := os.WriteFile(name, []byte(strings.Repeat("line\n", 4000)), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := exec.Command("git", "-C", env.Workspace, "add", "--all").Run(); err != nil {
			t.Skipf("git add is unavailable: %v", err)
		}
		if err := exec.Command("git", "-C", env.Workspace, "commit", "-m", "filler").Run(); err != nil {
			t.Skipf("git commit is unavailable: %v", err)
		}
	}

	output, err := runGit(context.Background(), env, "show", "--stat", "HEAD")
	if err != nil {
		t.Fatalf("runGit: %v", err)
	}
	if len(output) > maxGitOutputChars+64 {
		t.Errorf("output is %d chars, want it capped at %d", len(output), maxGitOutputChars)
	}
}

func TestFirstLineTakesTheSubject(t *testing.T) {
	if got := firstLine("fix the bug\n\nlonger body here"); got != "fix the bug" {
		t.Errorf("firstLine = %q", got)
	}
	if got := firstLine("  single line  "); got != "single line" {
		t.Errorf("firstLine = %q", got)
	}
	if got := firstLine("\n\nbody only"); got != "" {
		t.Errorf("firstLine = %q, want empty for a blank subject", got)
	}
}

// initGitRepo builds a committed repository, which is what the git tools need
// in order to have something to look at.
func initGitRepo(t *testing.T) *Env {
	t.Helper()
	env := testEnv(t)
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", env.Workspace}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Skipf("git %v is unavailable: %v (%s)", args, err, output)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Termixgo Test")
	if err := os.WriteFile(filepath.Join(env.Workspace, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "--all")
	run("commit", "-m", "initial commit")
	return env
}

// ------------------------------------------------- session storage

// TestSessionSaveReportsAnUnusableStateDirectory is the failure that matters:
// losing a conversation silently is worse than refusing to save it.
func TestSessionSaveReportsAnUnusableStateDirectory(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The sessions directory cannot be created under a file.
	t.Setenv(config.EnvHome, filepath.Join(blocker, "state"))

	session := NewSession(t.TempDir(), "test-model")
	session.AddUser("this must be reported")
	if err := session.Save(); err == nil {
		t.Fatalf("saving into an unusable directory must fail")
	}
}

func TestSessionSaveIsIdempotentAndKeepsTheID(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	session := NewSession(t.TempDir(), "test-model")
	session.AddUser("first")
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	id := session.ID()

	session.AddUser("second")
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if session.ID() != id {
		t.Errorf("the id changed across saves: %q then %q", id, session.ID())
	}
	loaded, err := LoadSession(id)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded.Turns() != 2 {
		t.Errorf("turns = %d, want 2", loaded.Turns())
	}
	// Exactly one file, so repeated saves replace rather than accumulate.
	list, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("sessions = %d, want 1", len(list))
	}
}

// ------------------------------------------------- registry execution

// TestExecuteReportsUnreadableArguments is the guard before any tool runs: a
// stream can be cut mid-arguments, and the model has to be told so it can
// resend instead of the call failing inside the tool.
func TestExecuteReportsUnreadableArguments(t *testing.T) {
	env := testEnv(t)
	runner := &Runner{Tools: DefaultRegistry(), Env: env, Policy: &ApprovalPolicy{Mode: ApprovalAll}}

	result := runner.execute(context.Background(), provider.ToolCall{Name: "read_file", Arguments: `{"path":`})
	if !result.IsError {
		t.Fatalf("truncated arguments must be refused, got %+v", result)
	}
	if !strings.Contains(result.Output, "read_file") {
		t.Errorf("output = %q, want it to name the tool", result.Output)
	}
}
