package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

// The system prompt must not change when the plan changes: it sits before the
// messages in every provider's cache prefix, so a changing plan there
// re-billed the whole history after every todo_write.
func TestSystemPromptStaysStaticAcrossPlanChanges(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{{{Type: provider.EventTextDelta, Text: "ok"}}}}
	runner, env, _ := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	before := runner.systemParts()
	if err := env.Todos.Write([]Todo{{ID: "1", Title: "Refactor the parser", Status: "in_progress"}}); err != nil {
		t.Fatal(err)
	}
	after := runner.systemParts()
	if len(after) != 1 || after[0] != before[0] || strings.Contains(after[0], "Refactor the parser") {
		t.Fatalf("the system prompt changed with the plan")
	}

	session := NewSession(env.Workspace, "test-model")
	if err := runner.Run(context.Background(), session, "continue"); err != nil {
		t.Fatal(err)
	}
	opening := session.Messages()[0].Content
	if !strings.Contains(opening, "Refactor the parser") {
		t.Fatalf("the turn's opening message should carry the plan:\n%s", opening)
	}
}

func TestTodoWriteResultCarriesThePlan(t *testing.T) {
	env := &Env{Workspace: t.TempDir(), Todos: NewTodoStore()}
	result, _ := (&todoWriteTool{}).Run(context.Background(), env, map[string]any{"todos": []any{
		map[string]any{"id": "1", "title": "Write tests", "status": "in_progress"},
		map[string]any{"id": "2", "title": "Ship", "status": "pending"},
	}})
	if result.IsError || !strings.Contains(result.Output, "[>] 1: Write tests") || !strings.Contains(result.Output, "[ ] 2: Ship") {
		t.Fatalf("result = %+v", result)
	}
}

// A discovered tool stays advertised for the rest of the session, and the
// list only grows at its end, so the tool prefix of the cache is stable
// between turns.
func TestDiscoveredToolsPersistAcrossTurnsInOrder(t *testing.T) {
	runner := &Runner{Tools: DefaultRegistry(), ToolSearch: true}
	session := NewSession(t.TempDir(), "m")
	session.AddDiscoveredTools([]string{"github_create_pr", "lsp_rename"})
	first := names(runner.stepTools(session.DiscoveredTools()))
	session.AddDiscoveredTools([]string{"generate_image", "github_create_pr"})
	second := names(runner.stepTools(session.DiscoveredTools()))

	if !strings.HasPrefix(second, first) {
		t.Fatalf("a new discovery must only append:\nfirst  %s\nsecond %s", first, second)
	}
	if !strings.HasSuffix(second, "github_create_pr,lsp_rename,generate_image") {
		t.Fatalf("discovered tools should follow the always-on set in discovery order: %s", second)
	}

	saved := sessionRoundTrip(t, session)
	if got := strings.Join(saved.DiscoveredTools(), ","); got != "github_create_pr,lsp_rename,generate_image" {
		t.Fatalf("the set should survive a save and load, got %q", got)
	}
}

func names(registry *Registry) string {
	var out []string
	for _, tool := range registry.Tools() {
		out = append(out, tool.Name())
	}
	return strings.Join(out, ",")
}

func sessionRoundTrip(t *testing.T, session *Session) *Session {
	t.Helper()
	dir := t.TempDir()
	session.SetStoreDir(dir)
	if err := session.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSessionIn(dir, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}
