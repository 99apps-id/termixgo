package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestShimmerGradientKeepsTheTextAndMoves(t *testing.T) {
	forceColor(t)
	styles := NewStyles(DefaultPalette())
	peak := shimmerPeak(styles)
	text := "Running go test ./..."
	seen := map[string]bool{}
	for frame := 0; frame < 30; frame++ {
		out := shimmerGradient(text, styles.Tool, peak, frame)
		if got := stripANSI(out); got != text {
			t.Fatalf("frame %d changed the text: %q", frame, got)
		}
		seen[out] = true
	}
	if len(seen) < 10 {
		t.Fatalf("the band should move across frames, only %d distinct renders", len(seen))
	}
	// Runs of one step are rendered together: far fewer escapes than runes.
	if escapes := strings.Count(shimmerGradient(text, styles.Tool, peak, 5), "\x1b["); escapes > 12 {
		t.Fatalf("%d escapes for one line; the runs should be batched", escapes)
	}
}

func TestShimmerPeakFollowsTheTheme(t *testing.T) {
	dark := shimmerPeak(NewStyles(DefaultPalette()))
	light := shimmerPeak(NewStyles(PaletteFor("light")))
	if dark.GetForeground() == light.GetForeground() {
		t.Fatalf("the peak should take the theme's text colour, got the same for dark and light")
	}
	_ = lipgloss.Color("")
}

func TestAnimationTickRunsOnlyDuringAnInlineTurn(t *testing.T) {
	model := inlineModel(t)
	model.pendingPrint = nil
	if _, cmd := model.Update(animationTickMsg{}); cmd != nil {
		t.Fatalf("an idle model must not keep animating")
	}
	model.running = true
	if _, cmd := model.Update(animationTickMsg{}); cmd == nil {
		t.Fatalf("a running inline turn keeps the animation going")
	}
}
