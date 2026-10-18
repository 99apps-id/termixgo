package agent

import (
	"fmt"
	"testing"
)

func TestFuzzySpellingsDirect(t *testing.T) {
	text := "package main\nfunc main() {}\n"
	needle := "FUNC MAIN()"
	pattern := fuzzyPattern(needle)
	fmt.Printf("pattern=%q\n", pattern)

	found := fuzzySpellings(text, pattern, true)
	t.Logf("found=%q", found)
	if len(found) != 1 {
		t.Fatalf("got %d matches", len(found))
	}
}
