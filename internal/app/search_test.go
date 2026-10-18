package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// TestAppWiresTheSearchIndex is the regression for a package that shipped
// complete and unreachable: search.Open was never called, so Env.Search stayed
// nil and search_memory always fell back to a substring scan.
func TestAppWiresTheSearchIndex(t *testing.T) {
	application := newTestApp(t)
	if application.search == nil {
		t.Fatalf("the app should open the full-text index")
	}
	// The database is state, not content, so it lives in the state directory
	// inside the workspace rather than in the files the model reads.
	if _, err := os.Stat(filepath.Join(application.Workspace(), ".termixgo", "search.db")); err != nil {
		t.Errorf("the index should live in .termixgo: %v", err)
	}
	// The tool environment is what search_memory reads, so a nil here means the
	// tool silently degrades no matter what the app holds.
	if env := application.env(); env.Search == nil {
		t.Errorf("Env.Search must be set so search_memory uses the index")
	}
}

// TestSearchMemoryFindsARememberedFact walks the path the operator uses: the
// agent stores a fact, then searches for it through the tool.
func TestSearchMemoryFindsARememberedFact(t *testing.T) {
	application := newTestApp(t)
	env := application.env()

	remember, ok := application.Tools().Lookup("remember")
	if !ok {
		t.Fatalf("the remember tool is missing")
	}
	if _, err := remember.Run(t.Context(), env, map[string]any{
		"fact":  "the smoke detector battery is in the hall cupboard",
		"scope": "project",
	}); err != nil {
		t.Fatalf("remember: %v", err)
	}

	search, ok := application.Tools().Lookup("search_memory")
	if !ok {
		t.Fatalf("the search_memory tool is missing")
	}
	result, err := search.Run(t.Context(), env, map[string]any{"query": "smoke detector"})
	if err != nil {
		t.Fatalf("search_memory: %v", err)
	}
	if !strings.Contains(result.Output, "FTS5") {
		t.Errorf("the search should have used the index, got:\n%s", result.Output)
	}
	if !strings.Contains(result.Output, "hall cupboard") {
		t.Errorf("the search should have found the fact, got:\n%s", result.Output)
	}
}

// TestRememberKeepsTheIndexInStep covers the write path: a fact stored after
// startup must be searchable without restarting the session.
func TestRememberKeepsTheIndexInStep(t *testing.T) {
	application := newTestApp(t)
	env := application.env()

	// Nothing is indexed yet.
	before, err := application.search.SearchScope("quantum", "memory", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("the index should start empty, got %d row(s)", len(before))
	}

	if err := env.Memory.Remember("prefer the quantum parser for that file", "project"); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	agent.IndexLearnedContent(application.search, env.Memory, env.Journal)

	after, err := application.search.SearchScope("quantum", "memory", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(after) == 0 {
		t.Errorf("the remembered fact should be indexed")
	}
}

// TestSearchIndexIsNotIndexedAsContent keeps the database out of its own index.
// A walk that reached .termixgo would index search.db, its WAL and the error
// journal, which is noise that grows with every search.
func TestSearchIndexIsNotIndexedAsContent(t *testing.T) {
	application := newTestApp(t)
	workspace := application.Workspace()

	if err := os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("a real document about zephyrs\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The workspace refresh is asynchronous; wait for it so the walk has
	// completed before the assertions below.
	application.search.SyncRefreshWorkspace()

	results, err := application.search.SearchScope("search", "workspace", 20)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, result := range results {
		if strings.HasPrefix(result.Path, ".termixgo") {
			t.Errorf("the state directory was indexed as content: %s", result.Path)
		}
	}
	// The real document must still be found, which proves the walk ran.
	found, err := application.search.SearchScope("zephyrs", "workspace", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) == 0 {
		t.Errorf("the workspace document should be indexed")
	}
}

// TestSearchDegradesWhenTheWorkspaceIsNotWritable documents the fallback: a
// state directory that cannot be created must not fail the session, only the
// index-backed search.
func TestSearchDegradesWhenTheWorkspaceIsNotWritable(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	workspace := t.TempDir()
	// A file where the state directory should be makes MkdirAll fail.
	if err := os.WriteFile(filepath.Join(workspace, ".termixgo"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	application, err := New(workspace)
	if err != nil {
		t.Fatalf("New must still succeed: %v", err)
	}
	t.Cleanup(application.Shutdown)
	if application.search != nil {
		t.Errorf("the index should be absent when its directory cannot be made")
	}

	// The tool still works through its substring fallback.
	search, ok := application.Tools().Lookup("search_memory")
	if !ok {
		t.Fatalf("the search_memory tool is missing")
	}
	result, err := search.Run(t.Context(), application.env(), map[string]any{"query": "anything"})
	if err != nil {
		t.Fatalf("search_memory should not fail without an index: %v", err)
	}
	if result.IsError {
		t.Errorf("the fallback should answer plainly, got %q", result.Output)
	}
}
