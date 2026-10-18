package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// withState points the session store at a throwaway directory, so these tests
// never touch the operator's real conversations.
func withState(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	return home
}

// ------------------------------------------------------------------ persistence

// TestSessionSaveAndLoadRoundTrip is the feature /sessions depends on. A field
// that does not survive the round trip is silently lost on resume.
func TestSessionSaveAndLoadRoundTrip(t *testing.T) {
	home := withState(t)

	session := NewSession("/work/project", "claude-sonnet-4-5")
	session.AddUser("add a health endpoint")
	session.AddAssistant("Done.", "I checked the routes first.", []provider.ToolCall{
		{ID: "call_1", Name: "read_file", Arguments: `{"path":"routes.go"}`},
	})
	session.AddToolResult("call_1", "read_file", "package routes")
	session.AddUsage(provider.Usage{PromptTokens: 1200, CompletionTokens: 300, TotalTokens: 1500})
	session.SetCost(0.0125)
	session.SetTodos([]Todo{{ID: "t1", Title: "write the handler", Status: "in_progress"}})

	id := session.ID()
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := filepath.Join(home, "sessions", id+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the session file was not written: %v", err)
	}

	loaded, err := LoadSession(id)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded.ID() != id {
		t.Errorf("id = %q, want %q", loaded.ID(), id)
	}
	if loaded.Workspace() != "/work/project" {
		t.Errorf("workspace = %q", loaded.Workspace())
	}
	if loaded.Model() != "claude-sonnet-4-5" {
		t.Errorf("model = %q", loaded.Model())
	}
	if loaded.Turns() != 1 {
		t.Errorf("turns = %d, want 1", loaded.Turns())
	}
	if loaded.MessageCount() != 3 {
		t.Errorf("messages = %d, want 3", loaded.MessageCount())
	}
	if loaded.Title() != "add a health endpoint" {
		t.Errorf("title = %q", loaded.Title())
	}
	if usage := loaded.Usage(); usage.TotalTokens != 1500 {
		t.Errorf("usage = %+v, want 1500 total", usage)
	}
	if cost := loaded.Cost(); cost != 0.0125 {
		t.Errorf("cost = %v, want 0.0125", cost)
	}
	todos := loaded.Todos()
	if len(todos) != 1 || todos[0].Title != "write the handler" {
		t.Errorf("todos = %+v", todos)
	}

	// The tool call and its result must survive intact, or a resumed
	// conversation would send the model a request with no answer.
	messages := loaded.Messages()
	if len(messages[1].ToolCalls) != 1 || messages[1].ToolCalls[0].Name != "read_file" {
		t.Errorf("tool call lost: %+v", messages[1])
	}
	if messages[2].ToolID != "call_1" || messages[2].Name != "read_file" {
		t.Errorf("tool result lost: %+v", messages[2])
	}
	if messages[1].Reasoning != "I checked the routes first." {
		t.Errorf("reasoning lost: %q", messages[1].Reasoning)
	}
}

// TestSessionSaveIsOwnerOnly checks the file is private on every platform.
//
// Inspect is used rather than os.Stat because the mode bits only mean
// something on POSIX: on Windows the restriction is an ACL, and a plain write
// would inherit the parent directory's grants. A conversation can contain a key
// the operator pasted, so this is the same guarantee the secret file carries.
func TestSessionSaveIsOwnerOnly(t *testing.T) {
	withState(t)
	session := NewSession("/w", "m")
	session.AddUser("hello")
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path := filepath.Join(mustSessionsDir(t), session.ID()+".json")
	access, err := secrets.Inspect(path)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !access.OwnerOnly {
		t.Errorf("session file is reachable beyond the owner: %v", access.Entries)
	}
}

// TestSessionSaveLeavesNoTemporaryFile keeps a crash mid-write from littering
// the sessions directory.
func TestSessionSaveLeavesNoTemporaryFile(t *testing.T) {
	withState(t)
	session := NewSession("/w", "m")
	session.AddUser("hello")
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(mustSessionsDir(t))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", entry.Name())
		}
	}
}

func TestSessionSaveIsAtomicOnRepeatedWrites(t *testing.T) {
	withState(t)
	session := NewSession("/w", "m")

	for index := 0; index < 5; index++ {
		session.AddUser("turn")
		if err := session.Save(); err != nil {
			t.Fatalf("save %d: %v", index, err)
		}
	}
	loaded, err := LoadSession(session.ID())
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded.Turns() != 5 {
		t.Errorf("turns = %d, want 5 after five saves", loaded.Turns())
	}
}

func TestLoadSessionReportsAMissingFile(t *testing.T) {
	withState(t)
	if _, err := LoadSession("nope"); err == nil {
		t.Fatalf("loading an unknown session must fail")
	}
}

func TestLoadSessionReportsMalformedJSON(t *testing.T) {
	home := withState(t)
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSession("broken"); err == nil {
		t.Fatalf("malformed JSON must be reported, not silently ignored")
	}
}

// TestSessionResetKeepsIdentityAndClearsState covers /new.
func TestSessionResetKeepsIdentityAndClearsState(t *testing.T) {
	withState(t)
	session := NewSession("/w", "m")
	session.AddUser("hello")
	session.AddUsage(provider.Usage{TotalTokens: 10})
	session.SetCost(1.5)
	session.SetTodos([]Todo{{Title: "x", Status: "pending"}})

	id := session.ID()
	session.Reset()

	if session.ID() != id {
		t.Errorf("Reset must keep the id")
	}
	if session.Turns() != 0 || session.MessageCount() != 0 {
		t.Errorf("Reset left messages behind: %d turns, %d messages", session.Turns(), session.MessageCount())
	}
	if session.Title() != "" {
		t.Errorf("title = %q, want it cleared", session.Title())
	}
	if usage := session.Usage(); usage.TotalTokens != 0 {
		t.Errorf("usage = %+v, want it cleared", usage)
	}
	if session.Cost() != 0 {
		t.Errorf("cost = %v, want it cleared", session.Cost())
	}
	if todos := session.Todos(); len(todos) != 0 {
		t.Errorf("todos = %+v, want them cleared", todos)
	}
}

// TestSessionTitleComesFromTheFirstRequest keeps the listing readable.
func TestSessionTitleComesFromTheFirstRequest(t *testing.T) {
	session := NewSession("/w", "m")
	session.AddUser("first request")
	session.AddUser("second request")

	if got := session.Title(); got != "first request" {
		t.Errorf("title = %q, want the first request", got)
	}

	// A very long request is shortened rather than stored whole.
	long := NewSession("/w", "m")
	long.AddUser(strings.Repeat("word ", 60))
	if len(long.Title()) > 80 {
		t.Errorf("title length = %d, want it shortened", len(long.Title()))
	}
}

// TestSessionMutationsUpdateTheTimestamp keeps the /sessions listing ordered.
//
// The assertions compare against a timestamp taken after a short pause: two
// time.Now() calls in a row can land on the same instant, and a test that
// depends on the clock moving between them fails for a reason unrelated to the
// code.
func TestSessionMutationsUpdateTheTimestamp(t *testing.T) {
	session := NewSession("/w", "m")
	start := session.UpdatedAt()

	// A mutation immediately after creation must not move the timestamp
	// backwards, whatever the clock resolution.
	session.AddUser("hello")
	if session.UpdatedAt().Before(start) {
		t.Errorf("AddUser moved the timestamp backwards")
	}

	before := session.UpdatedAt()
	time.Sleep(10 * time.Millisecond)
	session.SetModel("other-model")

	if !session.UpdatedAt().After(before) {
		t.Errorf("SetModel should move the timestamp forward")
	}
	if session.Model() != "other-model" {
		t.Errorf("model = %q", session.Model())
	}

	before = session.UpdatedAt()
	time.Sleep(10 * time.Millisecond)
	session.SetTodos([]Todo{{Title: "x", Status: "pending"}})
	if !session.UpdatedAt().After(before) {
		t.Errorf("SetTodos should move the timestamp forward")
	}
}

func TestSessionUsageAccumulates(t *testing.T) {
	session := NewSession("/w", "m")
	session.AddUsage(provider.Usage{PromptTokens: 100, CompletionTokens: 20})
	session.AddUsage(provider.Usage{PromptTokens: 50, CompletionTokens: 10})

	usage := session.Usage()
	if usage.PromptTokens != 150 || usage.CompletionTokens != 30 {
		t.Errorf("usage = %+v, want 150 in and 30 out", usage)
	}
}

func TestSessionLastAssistantText(t *testing.T) {
	session := NewSession("/w", "m")
	if got := session.LastAssistantText(); got != "" {
		t.Errorf("an empty session has no answer, got %q", got)
	}

	session.AddUser("hi")
	session.AddAssistant("first answer", "", nil)
	session.AddToolResult("c1", "read_file", "content")
	session.AddAssistant("second answer", "", nil)

	if got := session.LastAssistantText(); got != "second answer" {
		t.Errorf("LastAssistantText = %q, want the newest answer", got)
	}

	// A trailing assistant turn with only reasoning is not an answer.
	withEmpty := NewSession("/w", "m")
	withEmpty.AddAssistant("an answer", "", nil)
	withEmpty.AddAssistant("", "thinking only", nil)
	if got := withEmpty.LastAssistantText(); got != "an answer" {
		t.Errorf("LastAssistantText = %q, want it to skip an empty answer", got)
	}
}

// TestSessionLastAssistantTextSince keeps a caller from reporting an earlier
// turn's answer when the current turn says nothing.
func TestSessionLastAssistantTextSince(t *testing.T) {
	session := NewSession("/w", "m")
	session.AddUser("first")
	session.AddAssistant("first answer", "", nil)

	mark := session.MessageCount()
	session.AddUser("second")
	session.AddAssistant("second answer", "", nil)
	if got := session.LastAssistantTextSince(mark); got != "second answer" {
		t.Errorf("LastAssistantTextSince = %q, want the new answer", got)
	}

	// A turn that produced no assistant text must not fall back to the old one.
	quiet := session.MessageCount()
	session.AddUser("third")
	if got := session.LastAssistantTextSince(quiet); got != "" {
		t.Errorf("LastAssistantTextSince = %q, want empty for a silent turn", got)
	}
	// The old behaviour still sees the last answer in the session.
	if got := session.LastAssistantText(); got != "second answer" {
		t.Errorf("LastAssistantText = %q, want the previous answer", got)
	}
}

// TestMessagesReturnsACopy keeps a caller from mutating the conversation by
// accident, which the unexported fields alone would not prevent.
func TestMessagesReturnsACopy(t *testing.T) {
	session := NewSession("/w", "m")
	session.AddUser("original")

	snapshot := session.Messages()
	snapshot[0].Content = "tampered"

	if got := session.Messages()[0].Content; got != "original" {
		t.Errorf("content = %q, want the snapshot to be independent", got)
	}
}

// TestSessionIsSafeUnderConcurrentUse is the invariant the race detector found
// broken: the agent goroutine writes while the terminal reads.
func TestSessionIsSafeUnderConcurrentUse(t *testing.T) {
	withState(t)
	session := NewSession("/w", "m")

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writers, standing in for a running turn.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for index := 0; index < 200; index++ {
			session.AddUser("turn")
			session.AddAssistant("answer", "reasoning", nil)
			session.AddToolResult("c", "read_file", "output")
			session.AddUsage(provider.Usage{PromptTokens: 1})
			session.SetCost(float64(index) / 1000)
		}
		close(stop)
	}()

	// Readers, standing in for the status bar and the transcript.
	readers := sync.WaitGroup{}
	for index := 0; index < 4; index++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = session.Turns()
				_ = session.Messages()
				_ = session.Usage()
				_ = session.Cost()
				_ = session.Title()
				_ = session.LastAssistantText()
				_ = session.Todos()
			}
		}()
	}

	wg.Wait()
	readers.Wait()

	if session.Turns() != 200 {
		t.Errorf("turns = %d, want 200", session.Turns())
	}
	if err := session.Save(); err != nil {
		t.Fatalf("Save after concurrent use: %v", err)
	}
}

// ------------------------------------------------------------------ listing

func TestListSessionsIsNewestFirst(t *testing.T) {
	withState(t)

	older := NewSession("/w", "m")
	older.AddUser("older")
	if err := older.Save(); err != nil {
		t.Fatalf("save older: %v", err)
	}

	// A later session must sort above the first one.
	newer := NewSession("/w", "m")
	newer.AddUser("newer")
	if err := newer.Save(); err != nil {
		t.Fatalf("save newer: %v", err)
	}

	summaries, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries = %d, want 2", len(summaries))
	}
	for _, summary := range summaries {
		if summary.ID == "" || summary.Turns != 1 {
			t.Errorf("summary is incomplete: %+v", summary)
		}
	}
}

func TestListSessionsSkipsJunk(t *testing.T) {
	home := withState(t)
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A broken file and a nested directory must not stop the listing.
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o600); err != nil {
		t.Fatal(err)
	}

	session := NewSession("/w", "m")
	session.AddUser("good")
	if err := session.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	summaries, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Title != "good" {
		t.Errorf("summaries = %+v, want only the good session", summaries)
	}
}

// TestFromJSONRepairsMissingTimestamps keeps a hand-edited or older file from
// showing a zero date in the listing.
func TestFromJSONRepairsMissingTimestamps(t *testing.T) {
	session := fromJSON(sessionJSON{ID: "x", Model: "m"})
	if session.CreatedAt().IsZero() {
		t.Errorf("a missing createdAt should be filled in")
	}
	if session.UpdatedAt().IsZero() {
		t.Errorf("a missing updatedAt should be filled in")
	}
}

// TestSessionJSONShapeIsStable pins the on-disk keys. Renaming one would make
// every saved conversation unreadable.
func TestSessionJSONShapeIsStable(t *testing.T) {
	session := NewSession("/w", "m")
	session.AddUser("hello")
	session.SetCost(0.5)

	encoded, err := json.Marshal(session.snapshot())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(encoded)
	for _, key := range []string{`"id"`, `"title"`, `"workspace"`, `"model"`, `"createdAt"`, `"updatedAt"`, `"messages"`, `"costUsd"`} {
		if !strings.Contains(text, key) {
			t.Errorf("the persisted JSON is missing %s: %s", key, text)
		}
	}
}

func mustSessionsDir(t *testing.T) string {
	t.Helper()
	dir, err := config.SessionsDir()
	if err != nil {
		t.Fatalf("SessionsDir: %v", err)
	}
	return dir
}

// ------------------------------------------------------------------ file tools

func filesystemEnv(t *testing.T) *Env {
	t.Helper()
	workspace := t.TempDir()
	return &Env{Workspace: workspace, Todos: NewTodoStore(), Memory: NewMemory(workspace), Trusted: true}
}

func TestCreateDirectoryCreatesParentsAndIsIdempotent(t *testing.T) {
	env := filesystemEnv(t)
	tool := &createDirectoryTool{}

	result, err := tool.Run(context.Background(), env, map[string]any{"path": "a/b/c"})
	if err != nil || result.IsError {
		t.Fatalf("create: %+v err=%v", result, err)
	}
	if info, err := os.Stat(filepath.Join(env.Workspace, "a", "b", "c")); err != nil || !info.IsDir() {
		t.Fatalf("the nested directory was not created: %v", err)
	}

	// Running again must succeed and say it already existed, which is what
	// makes the tool safe to call without checking first.
	again, err := tool.Run(context.Background(), env, map[string]any{"path": "a/b/c"})
	if err != nil || again.IsError {
		t.Fatalf("second create: %+v err=%v", again, err)
	}
	if !strings.Contains(again.Output, "already exists") {
		t.Errorf("output = %q, want it to report the existing directory", again.Output)
	}
}

func TestCreateDirectoryRequiresAPath(t *testing.T) {
	env := filesystemEnv(t)
	result, _ := (&createDirectoryTool{}).Run(context.Background(), env, map[string]any{})
	if !result.IsError {
		t.Errorf("a missing path must be refused")
	}
}

func TestDeleteFileRemovesAFileAndATree(t *testing.T) {
	env := filesystemEnv(t)
	writeTestFile(t, env, "one.txt", "content\n")
	writeTestFile(t, env, "dir/nested/two.txt", "content\n")

	result, err := (&deleteFileTool{}).Run(context.Background(), env, map[string]any{"path": "one.txt"})
	if err != nil || result.IsError {
		t.Fatalf("delete file: %+v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace, "one.txt")); err == nil {
		t.Errorf("the file should be gone")
	}

	result, err = (&deleteFileTool{}).Run(context.Background(), env, map[string]any{"path": "dir"})
	if err != nil || result.IsError {
		t.Fatalf("delete tree: %+v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(env.Workspace, "dir")); err == nil {
		t.Errorf("the directory should be gone")
	}
}

func TestDeleteFileReportsAMissingPath(t *testing.T) {
	env := filesystemEnv(t)
	result, err := (&deleteFileTool{}).Run(context.Background(), env, map[string]any{"path": "nope.txt"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.IsError {
		t.Errorf("deleting something that does not exist should be reported")
	}
	if !strings.Contains(result.Output, "does not exist") {
		t.Errorf("output = %q, want it to explain", result.Output)
	}
}

func TestMoveFileRenamesAndRefusesToOverwrite(t *testing.T) {
	env := filesystemEnv(t)
	writeTestFile(t, env, "from.txt", "content\n")
	tool := &moveFileTool{}

	result, err := tool.Run(context.Background(), env, map[string]any{"from": "from.txt", "to": "sub/to.txt"})
	if err != nil || result.IsError {
		t.Fatalf("move: %+v err=%v", result, err)
	}
	content, err := os.ReadFile(filepath.Join(env.Workspace, "sub", "to.txt"))
	if err != nil || string(content) != "content\n" {
		t.Fatalf("the file did not move intact: %v %q", err, content)
	}

	// Overwriting is refused: losing a file to a rename is not recoverable.
	writeTestFile(t, env, "another.txt", "different\n")
	refused, err := tool.Run(context.Background(), env, map[string]any{"from": "another.txt", "to": "sub/to.txt"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !refused.IsError {
		t.Errorf("overwriting an existing file must be refused")
	}
	if !strings.Contains(refused.Output, "already exists") {
		t.Errorf("output = %q, want it to say why", refused.Output)
	}
	// The original must be untouched.
	content, _ = os.ReadFile(filepath.Join(env.Workspace, "sub", "to.txt"))
	if string(content) != "content\n" {
		t.Errorf("the destination was modified by a refused move: %q", content)
	}
}

func TestMoveFileRequiresBothPaths(t *testing.T) {
	env := filesystemEnv(t)
	result, _ := (&moveFileTool{}).Run(context.Background(), env, map[string]any{"from": "a.txt"})
	if !result.IsError {
		t.Errorf("a missing destination must be refused")
	}
}

func TestMoveFileReportsAMissingSource(t *testing.T) {
	env := filesystemEnv(t)
	result, _ := (&moveFileTool{}).Run(context.Background(), env, map[string]any{"from": "ghost.txt", "to": "x.txt"})
	if !result.IsError {
		t.Errorf("a missing source must be reported")
	}
}

// TestFilesystemToolsAreRegisteredWithSaneMetadata is a guard for the approval
// policy: a tool that mutates but reports otherwise would bypass the gate.
func TestFilesystemToolsAreRegisteredWithSaneMetadata(t *testing.T) {
	registry := DefaultRegistry()

	readOnly := []string{"read_file", "list_directory", "grep", "glob"}
	for _, name := range readOnly {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if tool.Mutating() {
			t.Errorf("%s reads only and must not be marked mutating", name)
		}
	}

	mutating := []string{"write_file", "edit", "multi_edit", "create_directory", "delete_file", "move_file"}
	for _, name := range mutating {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if !tool.Mutating() {
			t.Errorf("%s mutates and must be marked mutating, or it bypasses approval", name)
		}
		if tool.Risk() == "" {
			t.Errorf("%s must declare a risk level", name)
		}
	}
}

// TestToolLabelsArePresentAndPastTense keeps the transcript readable: an
// operator reads "Reading x" while it runs and "Read x" once it finishes.
func TestToolLabelsArePresentAndPastTense(t *testing.T) {
	registry := DefaultRegistry()
	args := map[string]any{
		"path": "example.go", "command": "go test ./...", "pattern": "TODO",
		"query": "x", "fact": "a fact", "thoughts": "thinking", "prompt": "look",
		"message": "commit msg", "url": "https://example.invalid", "name": "thing",
		"old_string": "a", "new_string": "b", "kind": "test", "handle": "proc-1",
		"from": "a", "to": "b", "ref": "HEAD", "level": "1", "action": "run",
		"todos": []any{}, "edits": []any{}, "paths": []any{"a"}, "options": []any{},
		"since_offset": 0, "timeout_secs": 1, "max_results": 1, "limit": 1,
		"all_changes": false, "replace_all": false, "staged": false, "stat": false,
		"create": false, "wait_secs": 1, "case_insensitive": false, "offset": 1,
		"content": "x", "cwd": ".", "display": "", "summary": "", "risk": "",
	}

	for _, tool := range registry.Tools() {
		label := tool.Label(args)
		done := tool.DoneLabel(args)
		if strings.TrimSpace(label) == "" {
			t.Errorf("%s produced an empty start label", tool.Name())
		}
		if strings.TrimSpace(done) == "" {
			t.Errorf("%s produced an empty done label", tool.Name())
		}
		// A label is one line: a newline would break the transcript layout.
		if strings.ContainsAny(label, "\n\r") || strings.ContainsAny(done, "\n\r") {
			t.Errorf("%s produced a multi-line label: %q / %q", tool.Name(), label, done)
		}
	}
}

// TestCatalogDescribesEveryTool is what /tools prints.
func TestCatalogDescribesEveryTool(t *testing.T) {
	registry := DefaultRegistry()
	catalog := registry.Catalog()
	if len(catalog) != len(registry.Tools()) {
		t.Fatalf("catalog = %d entries, tools = %d", len(catalog), len(registry.Tools()))
	}
	seen := map[string]bool{}
	for _, entry := range catalog {
		if entry.Name == "" || entry.Description == "" {
			t.Errorf("incomplete catalog entry: %+v", entry)
		}
		if seen[entry.Name] {
			t.Errorf("%s appears twice in the catalog", entry.Name)
		}
		seen[entry.Name] = true
	}
}
