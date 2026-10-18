package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestRendererTruncatePreservesWords feeds styled assistant lines through the
// exact truncation the Bubble Tea renderer applies per line
// (ansi.Truncate(line, width, "")) and requires the kept words to survive in
// order. A buggy cut here would garble settled frames.
func TestRendererTruncatePreservesWords(t *testing.T) {
	forceColor(t)
	styles := NewStyles(DefaultPalette())
	lines := []string{
		indent(renderMarkdown("Backend: Tauri 2 + Rust (`src-tauri/`), PTY via `portable-pty`.", styles, 118), "  "),
		indent(renderMarkdown("Package manager hanya `pnpm` (v11.9.0), Node >=22.", styles, 118), "  "),
		indent(renderMarkdown("Mau ngapain di termigo: baca arsitektur (`TERMIGO.md`), jalanin checks, fix sesuatu, atau fitur baru?", styles, 118), "  "),
	}
	for _, line := range lines {
		full := strings.Fields(stripANSI(line))
		for _, width := range []int{20, 40, 60, 80, 100, 120, 200} {
			cut := ansi.Truncate(line, width, "")
			stripped := stripANSI(cut)
			if strings.Contains(stripped, "\x1b") {
				t.Errorf("width %d cut a sequence:\n%q", width, cut)
			}
			got := strings.Fields(stripped)
			// Truncation may only cut the tail: every kept word but the last
			// must match exactly, and the last may be a cut fragment. Drops
			// or swaps in the middle are the garble signature.
			for index, word := range got {
				clean := strings.Trim(word, "`*")
				want := strings.Trim(full[index], "`*")
				if index < len(got)-1 {
					if clean != want {
						t.Errorf("width %d word %d = %q, want %q (line %q)", width, index, word, full[index], line)
						break
					}
					continue
				}
				if clean != want && !strings.HasPrefix(want, clean) {
					t.Errorf("width %d last word = %q, want %q or its cut (line %q)", width, word, full[index], line)
				}
			}
		}
	}
}
