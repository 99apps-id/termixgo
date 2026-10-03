package ui

import (
	"strings"
	"testing"
)

const sampleDiff = `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,3 @@
 package main
-func A() {}
+func A() int {}
 // tail
`

func TestParseUnifiedDiffNumbersRows(t *testing.T) {
	files := parseUnifiedDiff(sampleDiff)
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	if files[0].label != "main.go" {
		t.Errorf("label = %q, want main.go", files[0].label)
	}
	if files[0].adds != 1 || files[0].dels != 1 {
		t.Errorf("adds/dels = %d/%d, want 1/1", files[0].adds, files[0].dels)
	}
	rows := pairDiffRows(files[0].lines)
	paired := false
	for _, row := range rows {
		if row.span {
			continue
		}
		if row.left.kind == "del" && row.right.kind == "add" {
			paired = true
			if row.left.oldNum != 2 || row.right.newNum != 2 {
				t.Errorf("paired numbers = %d/%d, want 2/2", row.left.oldNum, row.right.newNum)
			}
		}
	}
	if !paired {
		t.Errorf("deletion and addition should share one visual row")
	}
}

func TestRenderDiffSideBySideKeepsBothColumns(t *testing.T) {
	files := parseUnifiedDiff(sampleDiff)
	out := renderDiffSideBySide(files, NewStyles(DefaultPalette()), 100, 50)
	plain := stripANSI(out)
	if !strings.Contains(plain, "main.go") {
		t.Errorf("missing file title:\n%s", plain)
	}
	if !strings.Contains(plain, "func A() {}") || !strings.Contains(plain, "func A() int {}") {
		t.Errorf("both sides should be visible:\n%s", plain)
	}
	if !strings.Contains(plain, "|") {
		t.Errorf("missing column separator:\n%s", plain)
	}
}

func TestRenderDiffUnifiedShowsNumbers(t *testing.T) {
	files := parseUnifiedDiff(sampleDiff)
	out := renderDiffUnified(files, NewStyles(DefaultPalette()), 100, 50)
	plain := stripANSI(out)
	if !strings.Contains(plain, "+") || !strings.Contains(plain, "-") {
		t.Errorf("missing change markers:\n%s", plain)
	}
	if !strings.Contains(plain, "2") {
		t.Errorf("missing line numbers:\n%s", plain)
	}
}

func TestDiffIsCleanReadsTheToolSentinel(t *testing.T) {
	for _, clean := range []string{"No changes.", "", "   \n"} {
		if !diffIsClean(clean) {
			t.Errorf("diffIsClean(%q) = false, want clean", clean)
		}
	}
	// A change that merely mentions cleaning something is still a change, and
	// showing nothing for it is the bug this guards.
	touched := "diff --git a/cleanup.go b/cleanup.go\n--- a/cleanup.go\n+++ b/cleanup.go\n@@ -1 +1 @@\n-void old();\n+void cleanup_now();\n"
	if diffIsClean(touched) {
		t.Errorf("a real diff must not be read as clean")
	}
}

func TestParseUnifiedDiffNeverLosesUnknownLines(t *testing.T) {
	files := parseUnifiedDiff("Binary files differ\n")
	if len(files) != 1 || len(files[0].lines) != 1 {
		t.Fatalf("unknown input should survive as one row, got %+v", files)
	}
}
