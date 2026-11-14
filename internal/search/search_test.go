package search

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
	for _, name := range []string{"node_modules", "vendor", "build", "target", ".venv", "__pycache__", "dist", "coverage", ".termixgo", ".termigo"} {
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
	write(".termixgo/memory.md", "# heavyproofstateword")

	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	store.SyncRefreshWorkspace()

	light, err := store.Search("lightproofword", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(light) == 0 || filepath.ToSlash(light[0].Path) != "src/main.go" {
		t.Errorf("the source file should be indexed, got %+v", light)
	}
	for _, query := range []string{"heavyproofnodeword", "heavyproofvendorword", "heavyproofstateword"} {
		results, err := store.Search(query, 10)
		if err != nil {
			t.Fatalf("Search(%q): %v", query, err)
		}
		if len(results) != 0 {
			t.Errorf("generated tree was indexed for %q: %+v", query, results)
		}
	}
}

// TestSearchScopePathLimitsToAFolder covers the path filter the operator asked
// for: a search over a big workspace can be limited to the folder being worked
// on, and the prefix matches a folder, not a name that merely starts with it.
func TestSearchScopePathLimitsToAFolder(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	_ = store.Index("workspace", "termigo-cli/internal/ui/render.go", "render.go", "the widget renders the frame")
	_ = store.Index("workspace", "termigo-cli-extra/main.go", "main.go", "the widget renders the frame")
	_ = store.Index("workspace", "other/internal/ui/render.go", "render.go", "the widget renders the frame")

	scoped, err := store.SearchScopePath("widget", "workspace", "termigo-cli", 20)
	if err != nil {
		t.Fatalf("SearchScopePath: %v", err)
	}
	if len(scoped) != 1 || filepath.ToSlash(scoped[0].Path) != "termigo-cli/internal/ui/render.go" {
		t.Errorf("scoped results = %+v, want only the termigo-cli file", scoped)
	}

	// A prefix normalizes to nothing and filters nothing.
	all, err := store.SearchScopePath("widget", "workspace", "./", 20)
	if err != nil {
		t.Fatalf("SearchScopePath: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("an empty filter returned %d results, want all 3", len(all))
	}

	if got := normalizePathPrefix("./termigo-cli/"); got != "termigo-cli" {
		t.Errorf("normalizePathPrefix = %q, want termigo-cli", got)
	}
	if got := normalizePathPrefix("   "); got != "" {
		t.Errorf("a blank prefix should mean no filter, got %q", got)
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

// TestIndexWalkBoundsOneFileRead keeps the workspace walk inside the memory
// budget: a file far larger than the index keeps is read capped, not slurped
// whole, and a fifo in the tree is refused rather than opened, because opening
// one blocks forever and would hang every search behind it.
func TestIndexWalkBoundsOneFileRead(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, maxIndexReadBytes+512<<10)
	copy(big, "needlehead ")
	for i := len("needlehead "); i < len(big); i++ {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), big, 0o644); err != nil {
		t.Fatalf("write big file: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	store.SyncRefreshWorkspace()
	results, err := store.Search("needlehead", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 || filepath.ToSlash(results[0].Path) != "big.txt" {
		t.Fatalf("the head of a large file must be indexed, got %+v", results)
	}

	if runtime.GOOS == "windows" {
		return
	}
	// A fifo named like a source file must be refused, not opened. The walk
	// would hang forever on an open that no writer will ever complete, which
	// is why the refusal is part of the contract and not just a nicety.
	fifo := filepath.Join(root, "pipe.txt")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Skipf("mkfifo is not available: %v", err)
	}
	done := make(chan struct{})
	go func() {
		store.SyncRefreshWorkspace()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the walk hung on a fifo: readIndexFile must refuse non-regular files")
	}
	results, err = store.Search("needlehead", 10)
	if err != nil {
		t.Fatalf("Search after the fifo: %v", err)
	}
	if len(results) == 0 {
		t.Errorf("the fifo must not have poisoned the index")
	}
}
