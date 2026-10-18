package ui

import (
	"testing"
)

// scrubComposer is the backstop for fragments the key gateway cannot see, and
// it may only remove shapes that cannot be real typing. A bare parameter tail
// such as "32M" or "35;106;27" is deliberately out of its scope: it is
// indistinguishable from code, so the gateway (which tracks report state) is
// what keeps it out.
func TestScrubComposerRemovesUnambiguousFragments(t *testing.T) {
	fragments := []struct {
		name string
		text string
	}{
		{"whole wheel report", "draft\x1b[<64;10;20M end"},
		{"introducer with its tail", "draft[<35;106;27M end"},
		{"introducer only", "draft[<35 end"},
		{"introducer cut early", "draft[< end"},
		{"semicolon tail", "draft;106;27M end"},
	}
	for _, fragment := range fragments {
		t.Run(fragment.name, func(t *testing.T) {
			model := chatModel(t)
			model.composer.SetValue(fragment.text)
			model.scrubComposer()
			got := model.composer.Value()
			if got != "draft end" {
				t.Errorf("composer = %q, want %q", got, "draft end")
			}
		})
	}
}

// A real draft must survive the backstop untouched, including the punctuation
// and identifiers a coder writes.
func TestScrubComposerKeepsRealDrafts(t *testing.T) {
	drafts := []string{
		"fix a<b and c<=d",
		"ports 1;2;3",
		"keep the M and the m letters",
		"if x < 35 { return }",
		"select * from t where id=32",
	}
	for _, draft := range drafts {
		t.Run(draft, func(t *testing.T) {
			model := chatModel(t)
			model.composer.SetValue(draft)
			model.scrubComposer()
			if got := model.composer.Value(); got != draft {
				t.Errorf("composer = %q, want it unchanged %q", got, draft)
			}
		})
	}
}
