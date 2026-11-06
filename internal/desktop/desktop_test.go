package desktop

import (
	"strings"
	"testing"
)

func TestNormalizeKeyCombo(t *testing.T) {
	cases := map[string][]string{
		"Ctrl+Shift+S": {"ctrl", "shift", "s"},
		"cmd space":    {"cmd", "space"},
		"return":       {"enter"},
		"option+tab":   {"alt", "tab"},
	}
	for input, want := range cases {
		got, err := normalizeKeyCombo(input)
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%q => %v, want %v", input, got, want)
		}
	}
	if _, err := normalizeKeyCombo("   "); err == nil {
		t.Fatal("an empty combo must fail")
	}
}

func TestNormalizeButton(t *testing.T) {
	if normalizeButton("RIGHT") != "right" || normalizeButton("middle") != "middle" {
		t.Fatal("button names must be case-insensitive")
	}
	if normalizeButton("sideways") != "left" {
		t.Fatal("an unknown button falls back to the left button")
	}
}

func TestParseWindows(t *testing.T) {
	raw := "0x03a00007\tfirefox\tDocs - Mozilla\r\n\n1234\tcode\tmain.go\n"
	windows := parseWindows(raw)
	if len(windows) != 2 {
		t.Fatalf("got %d windows: %+v", len(windows), windows)
	}
	if windows[0].ID != "0x03a00007" || windows[0].Title != "Docs - Mozilla" {
		t.Fatalf("first window = %+v", windows[0])
	}
	if windows[1].App != "code" || windows[1].Title != "main.go" {
		t.Fatalf("second window = %+v", windows[1])
	}
}

func TestRequirementErrorNamesTheTools(t *testing.T) {
	err := requirementError("screen capture", "grim", "scrot")
	if err == nil || !strings.Contains(err.Error(), "grim") || !strings.Contains(err.Error(), "scrot") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureOutputPath(t *testing.T) {
	if err := ensureOutputPath("  "); err == nil {
		t.Fatal("an empty capture path must fail")
	}
	if err := ensureOutputPath("/tmp/shot.png"); err != nil {
		t.Fatalf("a real path must pass: %v", err)
	}
}
