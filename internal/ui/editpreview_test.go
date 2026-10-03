package ui

import (
	"strings"
	"testing"
)

const samplePreview = "--- note.txt\n+++ note.txt\n before the line\n-hello from disk\n+hello changed\n after the line"

func TestToolBlockShowsColoredPreviewAfterSuccess(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	done := block{kind: blockTool, toolName: "edit", toolLabel: "Edited note.txt", toolOK: true, preview: samplePreview}

	compact := stripANSI(renderToolBlock(done, styles, 80, false))
	if !strings.Contains(compact, "-hello from disk") || !strings.Contains(compact, "+hello changed") {
		t.Errorf("compact transcript must show the changed lines: %q", compact)
	}
	if !strings.Contains(compact, "Edited note.txt") {
		t.Error("the label must stay on its own line above the preview")
	}
	// (colorization itself belongs to renderApprovalDiff's own tests; under
	// go test lipgloss detects a non-terminal and styles render plain)

	details := stripANSI(renderToolBlock(done, styles, 80, true))
	// At 80 columns the details view may be side-by-side (no +/- markers,
	// the columns carry the meaning); either way the changed text is there.
	if !strings.Contains(details, "hello changed") {
		t.Error("the details view must show the preview too")
	}
}

func TestToolBlockHidesPreviewWhileRunningOrFailed(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	running := block{kind: blockTool, toolName: "edit", toolLabel: "Editing note.txt", running: true, preview: samplePreview}
	if out := stripANSI(renderToolBlock(running, styles, 80, false)); strings.Contains(out, "hello changed") {
		t.Error("a running edit has changed nothing yet; the preview must wait")
	}
	failed := block{kind: blockTool, toolName: "edit", toolLabel: "Edit failed", toolOK: false, preview: samplePreview}
	if out := stripANSI(renderToolBlock(failed, styles, 80, true)); strings.Contains(out, "hello changed") {
		t.Error("a failed edit previews a change that never happened; it must stay hidden")
	}
}

func TestToolBlockCompactPeeksAndStaysBounded(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	var builder strings.Builder
	builder.WriteString("--- f\n+++ f\n")
	for line := 0; line < 30; line++ {
		builder.WriteString("+added line\n")
	}
	item := block{kind: blockTool, toolName: "edit", toolLabel: "Edited f", toolOK: true, preview: builder.String()}
	compact := stripANSI(renderToolBlock(item, styles, 80, false))
	seen := strings.Count(compact, "added line")
	if seen > previewPeekLines {
		t.Errorf("compact must peek at most %d preview lines, showed %d", previewPeekLines, seen)
	}
	if seen < 2 {
		t.Errorf("compact must show some of the diff, showed %d", seen)
	}
}

func TestToolBlockDetailsGoesSideBySideWhenWide(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	item := block{kind: blockTool, toolName: "edit", toolLabel: "Edited note.txt", toolOK: true, preview: samplePreview}

	wide := stripANSI(renderToolBlock(item, styles, 96, true))
	if !strings.Contains(wide, "|") {
		t.Errorf("the details view on a wide terminal must draw two columns, got %q", wide)
	}
	if !strings.Contains(wide, "hello from disk") || !strings.Contains(wide, "hello changed") {
		t.Error("both sides of the change must be visible side by side")
	}

	narrow := stripANSI(renderToolBlock(item, styles, 50, true))
	if !strings.Contains(narrow, "-hello from disk") || !strings.Contains(narrow, "+hello changed") {
		t.Error("a narrow terminal keeps the unified preview: half a code column is worse")
	}

	compact := stripANSI(renderToolBlock(item, styles, 96, false))
	if !strings.Contains(compact, "-hello from disk") {
		t.Error("the compact peek stays unified even on a wide terminal")
	}
}
