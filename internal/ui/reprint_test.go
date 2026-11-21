package ui

import "testing"

// The scrollback erase is printed through the renderer after the screen
// clear, never written to stdout beside it.
func TestReprintErasesScrollbackThroughTheRenderer(t *testing.T) {
	if got := len(reprintCmds("", false)); got != 1 {
		t.Fatalf("an empty reprint without erase is only the screen clear, got %d", got)
	}
	if got := len(reprintCmds("", true)); got != 2 {
		t.Fatalf("the erase must still be printed with no transcript, got %d", got)
	}
	if got := len(reprintCmds("hello", true)); got != 2 {
		t.Fatalf("clear then one print carrying erase and text, got %d", got)
	}
}
