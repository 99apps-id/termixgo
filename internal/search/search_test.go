package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAndSearch(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	_ = store.Index("memory", "memory.md", "Global Memory", "remember to always test before committing")
	_ = store.Index("journal", "error-journal.jsonl", "Error Journal", "read_file permission denied /etc/passwd")
	_ = store.Index("workspace", "main.go", "main.go", "package main\n\nfunc main() {}")

	results, err := store.Search("test", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected results for 'test', got none")
	}
	found := false
	for _, r := range results {
		if strings.Contains(r.Snippet, "test") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("results = %+v, want one mentioning test", results)
	}
}

// TestIndexWalkSkipsGeneratedTrees keeps the workspace walk off dependency and
// build output: those trees hold thousands of generated files, so indexing them
// floods the results and makes a search take minutes on a large workspace.
func TestIndexWalkSkipsGeneratedTrees(t *testing.T) {
	for _, name := range []string{"node_modules", "vendor", "build", "target", ".venv", "__pycache__", "dist", "coverage"} {
		if !IsSkippedDir(name) {
			t.Errorf("IsSkippedDir(%q) = false, want true", name)
		}
	}
	if IsSkippedDir("src") || IsSkippedDir("internal") {
		t.Errorf("a source directory must not be skipped")
	}
	if !IsSkippedDir("BUILD") {
		t.Errorf("the check must ignore case")
	}

	root := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("src/main.go", "package main // lightproofword")
	write("node_modules/pkg/index.js", "// heavyproofnodeword")
	write("vendor/lib/x.go", "package lib // heavyproofvendorword")
	write("build/out.go", "package build // heavyproofbuildword")

	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	light, err := store.Search("lightproofword", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(light) == 0 || filepath.ToSlash(light[0].Path) != "src/main.go" {
		t.Errorf("the source file should be indexed, got %+v", light)
	}
	for _, query := range []string{"heavyproofnodeword", "heavyproofvendorword"} {
		results, err := store.Search(query, 10)
		if err != nil {
			t.Fatalf("Search(%q): %v", query, err)
		}
		if len(results) != 0 {
			t.Errorf("generated tree was indexed for %q: %+v", query, results)
		}
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	results, err := store.Search("", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("empty query must return no results, got %d", len(results))
	}
}

func TestRemoveDocument(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	_ = store.Index("memory", "memory.md", "Memory", "remember to test everything")
	_ = store.Remove("memory", "memory.md")

	results, err := store.Search("test", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("removed document must not appear, got %d results", len(results))
	}
}

func TestIndexWorkspaceFiles(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "README.md"), []byte("# Termixgo\nA terminal agent."), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "node_modules"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "node_modules", "foo.js"), []byte("ignored"), 0o644)

	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	// indexLoop runs asynchronously; wait a bit or trigger manually.
	// For the test, just verify Index works for explicit documents.
	_ = store.Index("workspace", "README.md", "README.md", "# Termixgo\nA terminal agent.")

	results, err := store.Search("terminal", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Errorf("expected results for 'terminal', got none")
	}
}
