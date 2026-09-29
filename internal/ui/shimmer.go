package ui

import (
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// shimmerPeriod is how long one highlight sweep takes. The transcript
// refreshes on the 500ms tick while a turn runs, so each refresh lands on a
// new frame and the highlight visibly travels across the text.
const shimmerPeriod = 400 * time.Millisecond

// shimmerWidth is how many runes glow at once as the band passes.
const shimmerWidth = 4

// shimmerFrame is the current animation frame, derived from the clock so the
// render functions need no new parameters: every refresh while a turn runs
// lands on a later frame.
func shimmerFrame() int {
	return int(time.Now().UnixMilli() / shimmerPeriod.Milliseconds())
}

// shimmerEnabled reports whether the sweep may be drawn. Piped output, dumb
// terminals and NO_COLOR stay static: motion there is noise, not signal.
func shimmerEnabled() bool {
	if strings.TrimSpace(os.Getenv("NO_COLOR")) != "" {
		return false
	}
	if strings.ToLower(strings.TrimSpace(os.Getenv("TERM"))) == "dumb" {
		return false
	}
	return true
}

// shimmerLine draws text with a travelling highlight band. The content reads
// identically once colour is stripped, so tests and piped output see the
// same words with or without the effect.
func shimmerLine(text string, styles Styles) string {
	return shimmerWith(text, styles.Thinking, shimmerFrame())
}

// shimmerTool draws the running tool line with the same travelling band on
// the tool colour instead of the thinking colour.
func shimmerTool(text string, styles Styles) string {
	return shimmerWith(text, styles.Tool, shimmerFrame())
}

// shimmerWith is the deterministic core: frame picks the band position,
// which keeps the animation testable without sleeping.
func shimmerWith(text string, base lipgloss.Style, frame int) string {
	if !shimmerEnabled() {
		return base.Render(text)
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	span := len(runes) + shimmerWidth
	position := frame % span
	if position < 0 {
		position += span
	}
	bright := lipgloss.NewStyle().Foreground(lipgloss.Color("#f5f3ff")).Bold(true)
	var builder strings.Builder
	for index, char := range runes {
		distance := index - (position - shimmerWidth)
		if distance >= 0 && distance < shimmerWidth {
			builder.WriteString(bright.Render(string(char)))
			continue
		}
		builder.WriteString(base.Render(string(char)))
	}
	return builder.String()
}
