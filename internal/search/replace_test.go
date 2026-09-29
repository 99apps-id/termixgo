package search

import "testing"

// TestIndexReplacesTheSameDocument pins the key that keeps the index honest.
//
// The workspace walk re-indexes every file on each refresh and the memory tool
// re-indexes the memory files after every write. When Index appended instead of
// replacing, one file came back once per refresh, filled the result limit with
// its own duplicates, and grew the database without bound.
func TestIndexReplacesTheSameDocument(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	for attempt := 0; attempt < 3; attempt++ {
		if err := store.Index("workspace", "main.go", "main.go", "func main() { work() }"); err != nil {
			t.Fatalf("Index %d: %v", attempt, err)
		}
	}
	results, err := store.Search("work", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("one document indexed three times returned %d hits, want 1", len(results))
	}

	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM docs`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows = %d, want 1", rows)
	}
}

// TestIndexUpdatesTheBody checks that a replace really replaces: the old text
// must stop matching, or a stale copy would answer a search.
func TestIndexUpdatesTheBody(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.Index("memory", "memory.md", "Memory", "the old note about zebras"); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if err := store.Index("memory", "memory.md", "Memory", "the new note about alpacas"); err != nil {
		t.Fatalf("re-Index: %v", err)
	}
	stale, err := store.Search("zebras", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("the replaced body still matched: %+v", stale)
	}
	current, err := store.Search("alpacas", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(current) != 1 {
		t.Errorf("the new body matched %d times, want 1", len(current))
	}
}

// TestIndexEmptyBodyRemovesTheDocument covers a file that was emptied: keeping
// the previous body would report a note that no longer exists.
func TestIndexEmptyBodyRemovesTheDocument(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.Index("memory", "memory.md", "Memory", "a fact that will be deleted"); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if err := store.Index("memory", "memory.md", "Memory", ""); err != nil {
		t.Fatalf("Index empty: %v", err)
	}
	results, err := store.Search("deleted", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("an emptied document still matched: %+v", results)
	}
}
