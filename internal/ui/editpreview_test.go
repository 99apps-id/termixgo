package ui

import (
	"strings"
	"testing"
)

const samplePreview = "--- note.txt\n+++ note.txt\n before the line\n-hello from disk\n+hello changed\n after the line"

func TestToolBlockShowsBoxedPreviewAfterSuccess(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	done := block{kind: blockTool, toolName: "edit", toolLabel: "Edited note.txt", toolOK: true, preview: samplePreview}

	compact := stripANSI(renderToolBlock(done, styles, 80, false))
	for _, want := range []string{"- hello from disk", "+ hello changed", "note.txt", "╭", "╰"} {
		if !strings.Contains(compact, want) {
			t.Errorf("compact transcript preview missing %q:\n%s", want, compact)
		}
	}
	if !strings.Contains(compact, "Edited note.txt") {
		t.Error("the label must stay on its own line above the preview")
	}
	// The colour itself lives in the styles; under go test lipgloss detects a
	// non-terminal and renders plain, so the box and the markers are the
	// observable contract here.

	details := stripANSI(renderToolBlock(done, styles, 80, true))
	if !strings.Contains(details, "hello changed") || !strings.Contains(details, "╭") {
		t.Errorf("the details view must show the boxed preview too:\n%s", details)
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

func TestToolBlockPreviewStaysBoxedAtEveryWidth(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	item := block{kind: blockTool, toolName: "edit", toolLabel: "Edited note.txt", toolOK: true, preview: samplePreview}

	for _, width := range []int{96, 50} {
		out := stripANSI(renderToolBlock(item, styles, width, true))
		if !strings.Contains(out, "╭") || !strings.Contains(out, "╰") {
			t.Errorf("width %d: the details preview must be in a box, got:\n%s", width, out)
		}
		if !strings.Contains(out, "- hello from disk") || !strings.Contains(out, "+ hello changed") {
			t.Errorf("width %d: both the old and the new line must show, got:\n%s", width, out)
		}
	}

	compact := stripANSI(renderToolBlock(item, styles, 96, false))
	if !strings.Contains(compact, "- hello from disk") {
		t.Error("the compact peek keeps the same boxed preview")
	}
}
