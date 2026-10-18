package search

import (
	"fmt"
	"testing"
	"unicode/utf8"
)

// TestSearchAcceptsOperatorInput covers the query shapes an operator actually
// types. FTS5 reads punctuation as syntax, so "c++", "foo-bar", "status:1" and
// a lone "(" were all hard errors that made the whole search fail instead of
// returning the document that plainly contains them.
func TestSearchAcceptsOperatorInput(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	_ = store.Index("memory", "m.md", "Memory",
		"always run the checks before committing c++ code with foo-bar and status:1 markers")

	queries := []string{
		"c++", "foo-bar", "status:1", "checks", `"unterminated`,
		"a AND", "(", "*", "NOT x", "NEAR(a b)", "-", ".", "::", "--flag",
		"c++ code", "AND", "OR", "foo OR bar", "c++ AND checks",
	}
	for _, query := range queries {
		results, err := store.Search(query, 10)
		if err != nil {
			t.Errorf("Search(%q) returned %v; operator input must never be a syntax error", query, err)
			continue
		}
		_ = results
	}
}

// TestSearchStillFindsTheTerm pins that quoting did not cost recall: the
// punctuation-bearing query still returns the document that holds it.
func TestSearchStillFindsTheTerm(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	_ = store.Index("memory", "m.md", "Memory", "commit the c++ sources only after the checks pass")

	for _, query := range []string{"c++", "c++ sources"} {
		results, err := store.Search(query, 10)
		if err != nil {
			t.Fatalf("Search(%q): %v", query, err)
		}
		if len(results) == 0 {
			t.Errorf("Search(%q) lost the match the document plainly contains", query)
		}
	}
	// An OR keeps the wider meaning rather than becoming a required word.
	_ = store.Index("memory", "n.md", "Other", "unrelated note about rust")
	results, err := store.Search("c++ OR unrelated", 10)
	if err != nil {
		t.Fatalf("Search OR: %v", err)
	}
	if len(results) < 2 {
		t.Errorf("OR should reach both documents, got %d", len(results))
	}
}

// TestSearchScopeRestrictsInTheQuery covers the scope filter that used to be
// applied after the limit: with more matching workspace rows than the limit, a
// journal match that ranked lower was invisible and reported as no match.
func TestSearchScopeRestrictsInTheQuery(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	const phrase = "recurring failure marker in the log"
	for index := 0; index < 40; index++ {
		_ = store.Index("workspace", fmt.Sprintf("f%d.go", index), fmt.Sprintf("f%d.go", index), phrase)
	}
	_ = store.Index("journal", "error-journal.jsonl", "Error Journal", phrase)

	all, err := store.Search("failure", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	inTop20 := 0
	for _, result := range all {
		if result.Scope == "journal" {
			inTop20++
		}
	}
	if inTop20 > 0 {
		t.Skip("the fixture no longer pushes the journal row out of the top 20")
	}

	scoped, err := store.SearchScope("failure", "journal", 20)
	if err != nil {
		t.Fatalf("SearchScope: %v", err)
	}
	if len(scoped) != 1 {
		t.Fatalf("scoped search returned %d rows, want the one journal row", len(scoped))
	}
	if scoped[0].Scope != "journal" {
		t.Errorf("scoped search returned a %s row", scoped[0].Scope)
	}

	// "all" must stay equivalent to the unscoped call.
	every, err := store.SearchScope("failure", "all", 20)
	if err != nil {
		t.Fatalf("SearchScope all: %v", err)
	}
	if len(every) != len(all) {
		t.Errorf("scope all returned %d rows, want %d", len(every), len(all))
	}
}

// TestFtsQueryBuildsValidSyntax pins the builder directly, which is the layer
// that has to stay total: every input must produce either "" or valid syntax.
func TestFtsQueryBuildsValidSyntax(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"c++", `"c++"`},
		{"foo-bar", `"foo-bar"`},
		{"one two", `"one" AND "two"`},
		{"one OR two", `"one" OR "two"`},
		{"AND one", `"one"`},
		{"one AND", `"one"`},
		{"one AND OR two", `"one" OR "two"`},
		{"NOT one", `"one"`},
		{`he said "hi"`, `"he" AND "said" AND "hi"`},
		{`"`, ""},
	}
	for _, testCase := range cases {
		if got := ftsQuery(testCase.raw); got != testCase.want {
			t.Errorf("ftsQuery(%q) = %q, want %q", testCase.raw, got, testCase.want)
		}
	}
}

// TestIndexTruncatesOnARuneBoundary covers the byte cut: slicing at the cap
// used to land inside a multi-byte character and store invalid UTF-8.
func TestIndexTruncatesOnARuneBoundary(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	// A three-byte rune straddling the cap. It is appended as its encoded
	// bytes: a rune constant that fits in a byte would be written as that one
	// byte, which is not UTF-8 at all.
	body := make([]byte, 0, maxIndexBytes+8)
	for len(body) < maxIndexBytes-1 {
		body = append(body, 'a')
	}
	body = append(body, "世"...)
	body = append(body, []byte(" tail")...)

	if utf8.ValidString(string(body)) != true {
		t.Fatal("the fixture itself must be valid UTF-8")
	}
	if len(body) <= maxIndexBytes {
		t.Fatalf("the fixture must exceed the %d cap, got %d", maxIndexBytes, len(body))
	}

	clamped := clampIndexBytes(string(body))
	if len(clamped) > maxIndexBytes {
		t.Fatalf("clamped to %d bytes, over the %d cap", len(clamped), maxIndexBytes)
	}
	if !utf8.ValidString(clamped) {
		t.Errorf("the clamp split a rune and left invalid UTF-8")
	}
	if len(clamped) != maxIndexBytes-1 {
		t.Errorf("the clamp should back off to the rune boundary at %d, got %d", maxIndexBytes-1, len(clamped))
	}
}
