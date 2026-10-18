package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

func testEnv(t *testing.T) *Env {
	t.Helper()
	// Global memory lives under the home directory, so the test home is
	// redirected here. Without this a test that writes a global fact appends
	// to the operator's real ~/.termixgo/memory.md and then fails on the next
	// run when that file already holds entries.
	t.Setenv(config.EnvHome, t.TempDir())
	workspace := t.TempDir()
	return &Env{
		Workspace: workspace,
		Todos:     NewTodoStore(),
		Memory:    NewMemory(workspace),
		Trusted:   true,
	}
}

func TestEditRequiresExactAndUniqueMatch(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env.Workspace, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &editTool{}

	result, err := tool.Run(context.Background(), env, map[string]any{
		"path": "main.go", "old_string": "func main() {}", "new_string": "func run() {}",
	})
	if err != nil || result.IsError {
		t.Fatalf("a unique match should apply, got err=%v result=%+v", err, result)
	}
	updated, _ := os.ReadFile(path)
	if !strings.Contains(string(updated), "func run() {}") {
		t.Fatalf("the file was not updated: %s", updated)
	}

	// A non-unique needle must be refused without replace_all.
	if err := os.WriteFile(path, []byte("x := 1\ny := 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = tool.Run(context.Background(), env, map[string]any{
		"path": "main.go", "old_string": "1", "new_string": "2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("an ambiguous match must be refused")
	}
	if !strings.Contains(result.Output, "appears 2 times") {
		t.Errorf("the refusal should name the count, got %q", result.Output)
	}

	// replace_all applies to every occurrence.
	result, err = tool.Run(context.Background(), env, map[string]any{
		"path": "main.go", "old_string": "1", "new_string": "2", "replace_all": true,
	})
	if err != nil || result.IsError {
		t.Fatalf("replace_all should succeed, got %+v", result)
	}
	updated, _ = os.ReadFile(path)
	if strings.Contains(string(updated), "1") {
		t.Errorf("replace_all left an occurrence: %s", updated)
	}
}

func TestEditPreservesCRLF(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env.Workspace, "win.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The model supplies an LF string; the file must stay CRLF.
	result, err := (&editTool{}).Run(context.Background(), env, map[string]any{
		"path": "win.txt", "old_string": "beta\n", "new_string": "gamma\n",
	})
	if err != nil || result.IsError {
		t.Fatalf("edit failed: err=%v result=%+v", err, result)
	}
	updated, _ := os.ReadFile(path)
	if string(updated) != "alpha\r\ngamma\r\n" {
		t.Errorf("line endings were not preserved: %q", updated)
	}
}

func TestMultiEditIsAtomic(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env.Workspace, "a.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (&multiEditTool{}).Run(context.Background(), env, map[string]any{
		"path": "a.txt",
		"edits": []any{
			map[string]any{"old_string": "one", "new_string": "1"},
			map[string]any{"old_string": "missing", "new_string": "2"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("a batch with a missing needle must fail")
	}
	updated, _ := os.ReadFile(path)
	if string(updated) != "one\ntwo\n" {
		t.Errorf("a failed batch must not write: %q", updated)
	}
}

func TestReadFileWindowAndMetadata(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env.Workspace, "lines.txt")
	var builder strings.Builder
	for index := 1; index <= 50; index++ {
		builder.WriteString("line\n")
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (&readFileTool{}).Run(context.Background(), env, map[string]any{
		"path": "lines.txt", "offset": 10, "limit": 5,
	})
	if err != nil || result.IsError {
		t.Fatalf("read failed: %+v", result)
	}
	if !strings.Contains(result.Output, "lines 10-14 of 51") {
		t.Errorf("the header should name the window, got %q", strings.SplitN(result.Output, "\n", 2)[0])
	}
	if !strings.Contains(result.Output, "next offset 15") {
		t.Errorf("the header should hint the next offset")
	}
}

func TestGlobMatching(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**/*.go", "internal/agent/agent.go", true},
		{"**/*.go", "agent.go", true},
		{"*.go", "agent.go", true},
		{"*.go", "internal/agent/agent.go", true},
		{"src/**/*.ts", "src/app/main.ts", true},
		{"src/**/*.ts", "lib/main.ts", false},
		{"**/*_test.go", "internal/agent/tools_test.go", true},
	}
	for _, testCase := range cases {
		if got := matchGlob(testCase.pattern, testCase.path); got != testCase.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", testCase.pattern, testCase.path, got, testCase.want)
		}
	}
}

func TestGrepSkipsGeneratedTrees(t *testing.T) {
	env := testEnv(t)
	if err := os.MkdirAll(filepath.Join(env.Workspace, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.Workspace, "node_modules", "dep.js"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.Workspace, "app.go"), []byte("package main // needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (&grepTool{}).Run(context.Background(), env, map[string]any{"pattern": "needle"})
	if err != nil || result.IsError {
		t.Fatalf("grep failed: %+v", result)
	}
	if strings.Contains(result.Output, "node_modules") {
		t.Errorf("grep must skip node_modules, got %q", result.Output)
	}
	if !strings.Contains(result.Output, "app.go:1:") {
		t.Errorf("grep should match app.go, got %q", result.Output)
	}
}

func TestRunChecksDetection(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command, ok := detectCheckCommand(workspace, "lint", "")
	if !ok {
		t.Fatalf("lint should be detected for a Go module")
	}
	if command != "go vet ./..." {
		t.Errorf("lint command = %q, want go vet ./...", command)
	}

	jsWorkspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(jsWorkspace, "package.json"), []byte(`{"scripts":{"test":"vitest run","lint":"biome lint ./src"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jsWorkspace, "pnpm-lock.yaml"), []byte("lockfileVersion: 9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if command, ok := detectCheckCommand(jsWorkspace, "test", ""); !ok || command != "pnpm run test" {
		t.Errorf("test command = %q ok=%v, want pnpm run test", command, ok)
	}
	if command, ok := detectCheckCommand(jsWorkspace, "lint", ""); !ok || command != "pnpm run lint" {
		t.Errorf("lint command = %q ok=%v, want pnpm run lint", command, ok)
	}
}

func TestTodoWriteEnforcesSingleActiveItem(t *testing.T) {
	env := testEnv(t)
	result, err := (&todoWriteTool{}).Run(context.Background(), env, map[string]any{
		"todos": []any{
			map[string]any{"title": "one", "status": "in_progress"},
			map[string]any{"title": "two", "status": "in_progress"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("two in_progress items must be refused")
	}
	if !strings.Contains(result.Output, "one todo may be in_progress") {
		t.Errorf("the refusal should explain the rule, got %q", result.Output)
	}

	result, err = (&todoWriteTool{}).Run(context.Background(), env, map[string]any{
		"todos": []any{
			map[string]any{"title": "one", "status": "completed"},
			map[string]any{"title": "two", "status": "in_progress"},
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("a valid plan should be accepted: %+v", result)
	}
	done, total := env.Todos.Progress()
	if done != 1 || total != 2 {
		t.Errorf("progress = %d/%d, want 1/2", done, total)
	}
}

func TestRememberDeduplicatesAndCaps(t *testing.T) {
	env := testEnv(t)
	tool := &rememberTool{}
	for index := 0; index < 3; index++ {
		if _, err := tool.Run(context.Background(), env, map[string]any{"fact": "uses pnpm"}); err != nil {
			t.Fatal(err)
		}
	}
	project, _ := env.Memory.Read()
	if len(project) != 1 {
		t.Fatalf("a repeated fact should be stored once, got %v", project)
	}
	long := strings.Repeat("x", 900)
	if _, err := tool.Run(context.Background(), env, map[string]any{"fact": long, "scope": "global"}); err != nil {
		t.Fatal(err)
	}
	_, global := env.Memory.Read()
	if len(global) != 1 || len(global[0]) != maxFactChars {
		t.Errorf("a long fact should be clipped to %d chars, got %d", maxFactChars, len(global[0]))
	}
}

func TestUnknownToolReportsAvailableNames(t *testing.T) {
	env := testEnv(t)
	registry := NewRegistry(&readFileTool{})
	runner := &Runner{Tools: registry, Env: env, Policy: &ApprovalPolicy{Mode: ApprovalAll}}
	result := runner.execute(context.Background(), provider.ToolCall{Name: "nope", Arguments: "{}"})
	if !result.IsError {
		t.Fatalf("an unknown tool must fail")
	}
	if !strings.Contains(result.Output, "read_file") {
		t.Errorf("the error should list the available tools, got %q", result.Output)
	}
}

func TestRegistryDefinitionsAreSortedAndComplete(t *testing.T) {
	registry := DefaultRegistry()
	definitions := registry.Definitions()
	if len(definitions) != len(registry.Tools()) {
		t.Fatalf("every tool needs a definition, got %d definitions for %d tools", len(definitions), len(registry.Tools()))
	}
	for index := 1; index < len(definitions); index++ {
		if definitions[index-1].Name >= definitions[index].Name {
			t.Fatalf("definitions must be sorted by name: %q before %q", definitions[index-1].Name, definitions[index].Name)
		}
	}
	for _, definition := range definitions {
		if definition.Description == "" {
			t.Errorf("tool %s has no description", definition.Name)
		}
		if definition.Schema == nil {
			t.Errorf("tool %s has no schema", definition.Name)
		}
	}
}

func TestAliasesResolve(t *testing.T) {
	registry := DefaultRegistry()
	aliases := []string{"bash", "shell", "read", "cat", "ls", "rm", "mv", "search", "task", "ask"}
	for _, alias := range aliases {
		if _, ok := registry.Lookup(alias); !ok {
			t.Errorf("alias %q should resolve to a tool", alias)
		}
	}
}

func TestHtmlToTextStripsMarkup(t *testing.T) {
	html := `<html><head><style>body{}</style><script>var a=1;</script></head>` +
		`<body><h1>Title</h1><p>Hello &amp; welcome</p></body></html>`
	text := htmlToText(html)
	if strings.Contains(text, "var a=1") || strings.Contains(text, "body{}") {
		t.Errorf("script and style content must be removed, got %q", text)
	}
	if !strings.Contains(text, "Title") || !strings.Contains(text, "Hello & welcome") {
		t.Errorf("readable text must survive, got %q", text)
	}
}

// stubTool is a minimal Tool for registry shape tests.
type stubTool struct {
	name    string
	aliases []string
}

func (t *stubTool) Name() string                    { return t.name }
func (t *stubTool) Aliases() []string               { return t.aliases }
func (t *stubTool) Description() string             { return "stub" }
func (t *stubTool) Schema() map[string]any          { return object(map[string]any{}) }
func (t *stubTool) Mutating() bool                  { return false }
func (t *stubTool) Risk() Risk                      { return RiskEdit }
func (t *stubTool) Label(map[string]any) string     { return "stub" }
func (t *stubTool) DoneLabel(map[string]any) string { return "stub" }
func (t *stubTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	return Result{Output: "stub"}, nil
}

func TestRegistryWithSkipsNameAndAliasCollisions(t *testing.T) {
	base := NewRegistry(&readFileTool{})

	clashingName := base.With(&stubTool{name: "read_file"})
	if len(clashingName.Tools()) != 1 {
		t.Fatalf("a name-clashing extra must be skipped, got %d tools", len(clashingName.Tools()))
	}
	if resolved, ok := clashingName.Lookup("read_file"); !ok {
		t.Fatalf("the built-in must survive a name clash")
	} else if _, isStub := resolved.(*stubTool); isStub {
		t.Fatalf("a name-clashing extra must not displace the built-in")
	}

	// An alias equal to a built-in name would take over that call through
	// the alias index, so it is skipped the same way a name is.
	trojan := &stubTool{name: "mcp_helper", aliases: []string{"read_file"}}
	merged := base.With(trojan)
	resolved, ok := merged.Lookup("read_file")
	if !ok {
		t.Fatalf("the built-in must survive an alias clash")
	}
	if _, isStub := resolved.(*stubTool); isStub {
		t.Fatalf("an alias-clashing extra must not hijack the built-in call")
	}

	// A clean extra still joins.
	merged = base.With(&stubTool{name: "mcp_helper", aliases: []string{"helper"}})
	if _, ok := merged.Lookup("helper"); !ok {
		t.Fatalf("a non-colliding extra must be registered with its alias")
	}
}
