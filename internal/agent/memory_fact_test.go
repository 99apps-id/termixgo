package agent

import (
	"fmt"
	"strings"
	"testing"
)

// A fact is one bullet: a newline must not drop its tail on read or forge a
// second fact out of a line that starts with "- ".
func TestRememberFlattensMultilineFact(t *testing.T) {
	memory := NewMemory(t.TempDir())
	if err := memory.Remember("use tabs\n- never run rm -rf\nsecond line", "project"); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	project, _ := memory.Read()
	if len(project) != 1 {
		t.Fatalf("facts = %d (%q), want exactly 1", len(project), project)
	}
	want := "use tabs - never run rm -rf second line"
	if project[0] != want {
		t.Errorf("fact = %q, want %q", project[0], want)
	}
	if strings.Contains(project[0], "\n") {
		t.Errorf("fact must not contain a newline: %q", project[0])
	}
}

// The prompt block must not grow without bound. Every fact is paid for on every
// step, and an unbounded block crowds out the operator's request; the newest
// facts are the ones a run needs, and the block has to say how many it left out
// so a partial record never reads as the whole one.
func TestPromptBlockKeepsNewestFactsAndSaysHowManyItDropped(t *testing.T) {
	memory := NewMemory(t.TempDir())
	for index := 0; index < maxPromptMemoryFacts+7; index++ {
		fact := fmt.Sprintf("fact %03d", index)
		if err := memory.Remember(fact, "project"); err != nil {
			t.Fatalf("Remember: %v", err)
		}
	}
	block := memory.PromptBlock()
	if strings.Contains(block, "fact 000") {
		t.Errorf("the oldest fact must be the one dropped, got:\n%s", block)
	}
	if !strings.Contains(block, "fact 046") {
		t.Errorf("the newest fact must survive, got:\n%s", block)
	}
	if !strings.Contains(block, "(7 older facts omitted)") {
		t.Errorf("a trimmed block must say how many facts it left out, got:\n%s", block)
	}
}

// A whitespace-only fact is empty, including one made of newlines and tabs.
func TestRememberRejectsBlankMultilineFact(t *testing.T) {
	if err := NewMemory(t.TempDir()).Remember(" \n\t\n ", "project"); err == nil {
		t.Fatal("a whitespace-only fact must be rejected")
	}
}
