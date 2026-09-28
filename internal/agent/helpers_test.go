package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

// ------------------------------------------------- argument coercion
//
// The model does not always send the JSON type a schema asks for. A number
// arrives as a string, a boolean as the word "true", a small integer as a
// float. Every one of those has to be read rather than rejected, because the
// alternative is a tool call that fails for a reason the model cannot see.

func TestArgIntAcceptsEveryShapeTheModelSends(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  int
	}{
		{"a float from json", float64(7), 7},
		{"a go int", 7, 7},
		{"a json number", json.Number("7"), 7},
		{"a quoted number", " 7 ", 7},
		{"absent", nil, 5},
		{"an unparseable string", "seven", 5},
		{"an unparseable json number", json.Number("7.5"), 5},
		{"an unsupported type", []int{1}, 5},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			args := map[string]any{}
			if testCase.value != nil {
				args["n"] = testCase.value
			}
			if got := argInt(args, "n", 5, 0, 0); got != testCase.want {
				t.Errorf("argInt = %d, want %d", got, testCase.want)
			}
		})
	}
}

// TestArgIntClampsToTheDocumentedRange pins the bound. A limit is what keeps a
// runaway value from asking for a million results, so clamping is the whole
// point of the min and max arguments.
func TestArgIntClampsToTheDocumentedRange(t *testing.T) {
	if got := argInt(map[string]any{"n": float64(-40)}, "n", 5, 1, 10); got != 1 {
		t.Errorf("a value below the floor = %d, want 1", got)
	}
	if got := argInt(map[string]any{"n": float64(9999)}, "n", 5, 1, 10); got != 10 {
		t.Errorf("a value above the ceiling = %d, want 10", got)
	}
	// A max of zero means unbounded rather than "clamp to zero", which would
	// silently turn every offset into 0.
	if got := argInt(map[string]any{"n": float64(500)}, "n", 5, 0, 0); got != 500 {
		t.Errorf("with no ceiling the value should survive, got %d", got)
	}
}

func TestArgBoolAcceptsEveryShapeTheModelSends(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"a real bool", true, true},
		{"a false bool", false, false},
		{"the word true", "true", true},
		{"the word false", "false", false},
		{"a go-style capital", "True", true},
		{"absent falls back", nil, true},
		{"nonsense falls back", "maybe", true},
		{"a number falls back", float64(1), true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			args := map[string]any{}
			if testCase.value != nil {
				args["b"] = testCase.value
			}
			if got := argBool(args, "b", true); got != testCase.want {
				t.Errorf("argBool = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestToStringRendersEveryJSONShape(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"a string is itself", "text", "text"},
		{"nil is empty", nil, ""},
		{"true", true, "true"},
		{"false", false, "false"},
		{"a whole float has no point", float64(3), "3"},
		{"a fractional float keeps it", float64(1.5), "1.5"},
		{"a json number survives", json.Number("42"), "42"},
		{"an object is marshalled", map[string]any{"a": 1}, `{"a":1}`},
		{"an array is marshalled", []any{1, 2}, "[1,2]"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := toString(testCase.value); got != testCase.want {
				t.Errorf("toString(%#v) = %q, want %q", testCase.value, got, testCase.want)
			}
		})
	}
	// A value that cannot be marshalled must not panic. A channel is the
	// easiest one to produce, and a tool argument could be anything.
	if got := toString(make(chan int)); got != "" {
		t.Errorf("toString of an unencodable value = %q, want empty", got)
	}
}

func TestArgStringPrefersTheFirstUsableAlias(t *testing.T) {
	args := map[string]any{"path": "", "file": "main.go", "id": 7}
	if got := argString(args, "path", "file"); got != "main.go" {
		t.Errorf("a blank alias should be skipped, got %q", got)
	}
	// A number is a legitimate identifier for a handle or an id.
	if got := argString(args, "id"); got != "7" {
		t.Errorf("argString of a number = %q, want 7", got)
	}
	if got := argString(args, "nothing", "alsoNothing"); got != "" {
		t.Errorf("argString = %q, want empty", got)
	}
	if got := argString(map[string]any{"n": json.Number("9")}, "n"); got != "9" {
		t.Errorf("argString of a json number = %q, want 9", got)
	}
}

func TestArgListCollectsOnlyStrings(t *testing.T) {
	// A list is coerced item by item rather than filtered, because a model
	// offering choices often sends numbers: [3000, 8080] must reach the
	// operator as two ports, not as an empty list. Blanks are dropped.
	got := argList(map[string]any{"options": []any{"a", " b ", "", 3}}, "options")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "3" {
		t.Errorf("argList = %v, want a, b and the number as text", got)
	}
	// A comma-separated string is accepted because a model often sends that
	// instead of an array.
	fromText := argList(map[string]any{"options": "a, b ,,c"}, "options")
	if len(fromText) != 3 || fromText[2] != "c" {
		t.Errorf("argList of a string = %v, want three parts", fromText)
	}
	if argList(map[string]any{}, "options") != nil {
		t.Errorf("a missing key should list nothing")
	}
	if argList(map[string]any{"options": 42}, "options") != nil {
		t.Errorf("a number should list nothing")
	}
	if got := argList(map[string]any{"options": []string{"x"}}, "options"); len(got) != 1 {
		t.Errorf("a typed slice should work, got %v", got)
	}
	// The first usable alias wins, matching argString.
	aliased := argList(map[string]any{"a": nil, "b": []any{"kept"}}, "a", "b")
	if len(aliased) != 1 || aliased[0] != "kept" {
		t.Errorf("argList = %v, want the later alias used", aliased)
	}
}

func TestSchemaBuildersProduceUsableJSONSchema(t *testing.T) {
	schema := object(map[string]any{
		"path":  strProp("the file"),
		"lines": intProp("how many"),
		"all":   boolProp("everything"),
		"tags":  arrayProp("labels", strProp("one label")),
	}, "path", "lines")

	if schema["type"] != "object" {
		t.Errorf("type = %#v, want object", schema["type"])
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 2 || required[0] != "path" {
		t.Fatalf("required = %#v, want path and lines", schema["required"])
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", schema["properties"])
	}
	if len(properties) != 4 {
		t.Errorf("properties = %d, want four", len(properties))
	}
	// Every property must carry a description and a type, because that is what
	// the model reads to decide how to fill the call.
	for name, raw := range properties {
		property, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("%s = %#v", name, raw)
		}
		if property["type"] == "" || property["type"] == nil {
			t.Errorf("%s has no type: %#v", name, property)
		}
		if strings.TrimSpace(toString(property["description"])) == "" {
			t.Errorf("%s has no description", name)
		}
	}
	tags, _ := properties["tags"].(map[string]any)
	if tags["type"] != "array" {
		t.Errorf("tags = %#v, want an array", tags)
	}
	if _, ok := tags["items"].(map[string]any); !ok {
		t.Errorf("an array needs an items schema, got %#v", tags["items"])
	}

	// A schema with no required list must still be a valid object, since that
	// is what a tool with only optional arguments sends.
	optional := object(map[string]any{"path": strProp("the file")})
	if optional["type"] != "object" {
		t.Errorf("type = %#v", optional["type"])
	}
}

func TestResolvePathHandlesTildeAbsoluteAndRelative(t *testing.T) {
	env := &Env{Workspace: t.TempDir()}

	if got := resolvePath(env, "  "); got != "" {
		t.Errorf("a blank path = %q, want empty", got)
	}
	if got := resolvePath(env, "sub/file.go"); got != filepath.Join(env.Workspace, "sub", "file.go") {
		t.Errorf("a relative path = %q, want it under the workspace", got)
	}
	absolute := filepath.Join(t.TempDir(), "elsewhere", "..", "here")
	if got := resolvePath(env, absolute); got != filepath.Clean(absolute) {
		t.Errorf("an absolute path = %q, want it cleaned", got)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory to expand: %v", err)
	}
	if got := resolvePath(env, "~/notes.md"); got != filepath.Join(home, "notes.md") {
		t.Errorf("a tilde path = %q, want it expanded", got)
	}
	if got := resolvePath(env, "~"); got != home {
		t.Errorf("a bare tilde = %q, want the home directory", got)
	}

	// With no workspace there is nothing to join against, so the path stands
	// on its own rather than becoming absolute against the process directory.
	bare := &Env{}
	if got := resolvePath(bare, "file.go"); got != "file.go" {
		t.Errorf("without a workspace = %q, want the path unchanged", got)
	}
}

func TestDisplayPathMakesAPathRelative(t *testing.T) {
	env := &Env{Workspace: t.TempDir()}
	inside := filepath.Join(env.Workspace, "internal", "agent", "loop.go")
	if got := displayPath(env, inside); got != "internal/agent/loop.go" {
		t.Errorf("displayPath = %q, want a slash-separated relative path", got)
	}
	// Outside the workspace there is no useful relative form, so the absolute
	// path is kept rather than showing a trail of "..".
	outside := filepath.Join(t.TempDir(), "other.go")
	if got := displayPath(env, outside); got != outside {
		t.Errorf("displayPath = %q, want the absolute path kept", got)
	}
	if got := displayPath(&Env{}, inside); got != inside {
		t.Errorf("without a workspace = %q, want the path unchanged", got)
	}
}

// TestOpenErrorTellsTheModelWhatWentWrong pins the three cases apart. A single
// "file not found" for a permission problem sends the model down the wrong
// path: it starts creating a file that already exists.
func TestOpenErrorTellsTheModelWhatWentWrong(t *testing.T) {
	missing := openError(os.ErrNotExist, "a.go")
	if !strings.Contains(missing, "does not exist") {
		t.Errorf("missing = %q, want it to say the file is absent", missing)
	}
	denied := openError(os.ErrPermission, "b.go")
	if !strings.Contains(denied, "permission denied") {
		t.Errorf("denied = %q, want it to say the permission", denied)
	}
	other := openError(os.ErrInvalid, "c.go")
	if !strings.Contains(other, "c.go") || !strings.Contains(other, "invalid") {
		t.Errorf("other = %q, want the path and the cause", other)
	}
}

// ------------------------------------------------- shell selection

// TestShellInvocationMatchesThePlatform is the one place the production shell
// is asserted. Every other test substitutes a cheap shell, so a mistake here
// would ship without any test noticing.
func TestShellInvocationMatchesThePlatform(t *testing.T) {
	command := "echo hello"
	shell, args := shellInvocation(command)

	if shell == "" {
		t.Fatalf("no shell was chosen")
	}
	if len(args) == 0 || args[len(args)-1] != command {
		t.Fatalf("the command must be the last argument, got %v", args)
	}
	if runtime.GOOS == "windows" {
		if !strings.Contains(strings.ToLower(shell), "powershell") {
			t.Errorf("shell = %q, want PowerShell on Windows", shell)
		}
		for _, flag := range []string{"-NoProfile", "-NonInteractive", "-Command"} {
			found := false
			for _, arg := range args {
				if arg == flag {
					found = true
				}
			}
			if !found {
				t.Errorf("args = %v, want %s so a profile cannot change the result", args, flag)
			}
		}
		return
	}
	if !strings.HasSuffix(shell, "sh") {
		t.Errorf("shell = %q, want a POSIX shell", shell)
	}
	if args[0] != "-c" && args[0] != "-lc" {
		t.Errorf("args = %v, want a command flag first", args)
	}
}

// ------------------------------------------------- todos

// TestTodoStoreSetRestoresWithoutValidation is the resume path: a saved plan
// comes back exactly as it was, including a shape Write would reject. That is
// deliberate, because a session must not lose its plan to a rule change.
func TestTodoStoreSetRestoresWithoutValidation(t *testing.T) {
	store := NewTodoStore()
	// Two in_progress items and a blank title: Write refuses all of this.
	items := []Todo{
		{ID: "1", Title: "", Status: "in_progress"},
		{ID: "2", Title: "second", Status: "in_progress"},
	}
	store.Set(items)

	got := store.Items()
	if len(got) != 2 {
		t.Fatalf("Items = %+v, want both restored", got)
	}
	if got[0].Title != "" || got[1].Status != "in_progress" {
		t.Errorf("the plan was altered: %+v", got)
	}

	// The stored slice must be a copy, or the caller could keep mutating the
	// store through the slice it passed in.
	items[0].Title = "changed outside"
	if store.Items()[0].Title != "" {
		t.Errorf("Set kept a reference to the caller's slice")
	}
	// And Items must hand back a copy for the same reason.
	snapshot := store.Items()
	snapshot[0].Title = "changed again"
	if store.Items()[0].Title != "" {
		t.Errorf("Items handed out its own slice")
	}

	// Setting an empty list clears the plan, which is what a fresh session does.
	store.Set(nil)
	if len(store.Items()) != 0 {
		t.Errorf("Set(nil) = %+v, want an empty plan", store.Items())
	}
}

// ------------------------------------------------- process notices

// noticeCollector gathers emitter notices safely.
//
// The manager emits from its own watcher goroutine, and the exit notice can
// arrive after the process's done channel is closed, so collecting without a
// lock races with the assertion that reads the list.
type noticeCollector struct {
	mu      sync.Mutex
	notices []string
}

func (c *noticeCollector) emit(event Event) {
	if event.Kind != EventNotice {
		return
	}
	c.mu.Lock()
	c.notices = append(c.notices, event.Text)
	c.mu.Unlock()
}

func (c *noticeCollector) joined() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.notices, "\n")
}

func (c *noticeCollector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.notices)
}

// waitForNotice polls until the collected notices contain a needle, which is
// how a test waits for a goroutine it does not own.
func waitForNotice(t *testing.T, collector *noticeCollector, needle string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(collector.joined(), needle) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no notice contained %q; collected:\n%s", needle, collector.joined())
}

// TestSetEmitterWiresTheNotices is the contract the app depends on: a process
// outlives the run that started it, so the manager keeps its own sink and the
// operator still sees the start and exit lines.
func TestSetEmitterWiresTheNotices(t *testing.T) {
	manager := newTestManager()
	defer manager.Shutdown()

	collector := &noticeCollector{}
	manager.SetEmitter(collector.emit)

	process, err := manager.Start(context.Background(), quickCommand("emitted"), t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-process.done

	waitForNotice(t, collector, process.ID)
	waitForNotice(t, collector, "Started")

	// Replacing the emitter must take effect, which is what lets a later run
	// own the events after the first one is gone.
	replacement := &noticeCollector{}
	manager.SetEmitter(replacement.emit)
	if _, err := manager.Start(context.Background(), quickCommand("second"), t.TempDir()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForNotice(t, replacement, "Started")
	// The old sink must stop receiving, or two surfaces would report the same
	// process and the operator would see every line twice.
	before := collector.count()
	time.Sleep(100 * time.Millisecond)
	if collector.count() != before {
		t.Errorf("the replaced emitter kept receiving notices")
	}
}

// ------------------------------------------------- approval mode

// TestApprovalModeOrDefaultFailsSafe is a security check: an unreadable or
// hand-edited mode must not be read as "allow everything" by accident, and the
// two strict modes must survive the translation.
func TestApprovalModeOrDefaultFailsSafe(t *testing.T) {
	cases := map[config.ApprovalMode]ApprovalMode{
		config.ApprovalAsk:   ApprovalAsk,
		config.ApprovalEdits: ApprovalEdits,
		config.ApprovalAll:   ApprovalAll,
		"":                   ApprovalAll,
		"sometimes":          ApprovalAll,
		"ASK":                ApprovalAll,
	}
	for configured, want := range cases {
		cfg := config.Default()
		cfg.ApprovalMode = configured
		if got := ApprovalModeOrDefault(cfg); got != want {
			t.Errorf("ApprovalModeOrDefault(%q) = %q, want %q", configured, got, want)
		}
	}
}

// ------------------------------------------------- project memory

// TestProjectMemoryReachesThePrompt is why the file exists: a repository that
// ships an AGENTS.md must have its conventions in the system prompt without
// the operator configuring anything.
func TestProjectMemoryReachesThePrompt(t *testing.T) {
	env := testEnv(t)
	document := "# House rules\n\n- No em-dashes.\n"
	if err := os.WriteFile(filepath.Join(env.Workspace, "AGENTS.md"), []byte(document), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	prompt := BuildSystem(env, "")
	if !strings.Contains(prompt, "## PROJECT MEMORY - AGENTS.md") {
		t.Errorf("the prompt should name the file it read:\n%s", prompt)
	}
	if !strings.Contains(prompt, "No em-dashes.") {
		t.Errorf("the prompt should carry the content:\n%s", prompt)
	}
}

// TestProjectMemoryPrefersTheFirstNameInOrder pins the precedence, because a
// repository with two memory files must give a predictable answer.
func TestProjectMemoryPrefersTheFirstNameInOrder(t *testing.T) {
	env := testEnv(t)
	for index, name := range projectMemoryFiles {
		content := "# from " + name + "\n"
		if err := os.WriteFile(filepath.Join(env.Workspace, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if index > 0 {
			// Later files must not win, so the earlier one must not be
			// overshadowed by the loop above having written it too.
			continue
		}
	}

	if got := memoryName(env.Workspace); got != projectMemoryFiles[0] {
		t.Errorf("memoryName = %q, want %q", got, projectMemoryFiles[0])
	}
	memory := readProjectMemory(env.Workspace)
	if !strings.Contains(memory, projectMemoryFiles[0]) {
		t.Errorf("read = %q, want the first file", memory)
	}
	if strings.Contains(memory, projectMemoryFiles[len(projectMemoryFiles)-1]) {
		t.Errorf("read = %q, want only the first file", memory)
	}
}

// TestMemoryNameFallsBackToThePreferredDefault keeps the heading stable when a
// repository has no memory file at all.
func TestMemoryNameFallsBackToThePreferredDefault(t *testing.T) {
	if got := memoryName(t.TempDir()); got != projectMemoryFiles[0] {
		t.Errorf("memoryName = %q, want the default name", got)
	}
}

func TestReadProjectMemoryCapsALargeFile(t *testing.T) {
	env := testEnv(t)
	big := strings.Repeat("x", projectMemoryCap+5000)
	if err := os.WriteFile(filepath.Join(env.Workspace, projectMemoryFiles[0]), []byte(big), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	memory := readProjectMemory(env.Workspace)
	if len(memory) > projectMemoryCap+64 {
		t.Errorf("read %d bytes, want it capped at %d", len(memory), projectMemoryCap)
	}
	if !strings.HasSuffix(memory, "[truncated]") {
		t.Errorf("a capped file must say so, got the tail %q", memory[len(memory)-30:])
	}
	// A blank workspace has nothing to read and must not panic on the join.
	if readProjectMemory("   ") != "" {
		t.Errorf("a blank workspace should read nothing")
	}
}

// TestProjectMemorySkipsAnUnreadableFile covers a directory where a memory file
// is expected, which is a realistic leftover from a botched checkout.
func TestProjectMemorySkipsAnUnreadableFile(t *testing.T) {
	env := testEnv(t)
	if err := os.MkdirAll(filepath.Join(env.Workspace, projectMemoryFiles[0]), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// The next name in the list is used instead of failing the whole prompt.
	if err := os.WriteFile(filepath.Join(env.Workspace, projectMemoryFiles[1]), []byte("# fallback\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := readProjectMemory(env.Workspace); !strings.Contains(got, "fallback") {
		t.Errorf("read = %q, want it to move on to the next name", got)
	}
}
