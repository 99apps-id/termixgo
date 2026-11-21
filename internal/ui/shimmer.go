package ui

import (
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// shimmerPeriod is how long the band takes to move one rune. The inline
// layout repaints on its own animation tick while a turn runs, so at 60ms a
// twenty-rune verb is swept in a little over a second, which reads as a smooth
// glint rather than the jumpy crawl a 400ms step on a 500ms repaint gave.
const shimmerPeriod = 60 * time.Millisecond

// animationInterval is the inline layout's repaint rate while a turn runs. It
// only redraws the live area, so it costs little; the full-screen layout
// rebuilds its whole transcript per paint and keeps the slower tick.
const animationInterval = 80 * time.Millisecond

type animationTickMsg struct{}

func animationTick() tea.Cmd {
	return tea.Tick(animationInterval, func(time.Time) tea.Msg { return animationTickMsg{} })
}

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
	return shimmerGradient(text, styles.Thinking, shimmerPeak(styles), shimmerFrame())
}

// shimmerTool draws the running tool line with the same travelling band on
// the tool colour instead of the thinking colour.
func shimmerTool(text string, styles Styles) string {
	return shimmerGradient(text, styles.Tool, shimmerPeak(styles), shimmerFrame())
}

// shimmerPeak is the brightest step of the band: the theme's own text
// colour, so it stands out on a dark background and on a light one alike.
func shimmerPeak(styles Styles) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(styles.Palette.Text).Bold(true)
}

// shimmerGradient draws a three-step band: the peak in the middle, the base
// colour in bold on each side, and the plain base elsewhere. Runs of one step
// are rendered together, so a line carries a handful of escapes rather than
// one per rune.
func shimmerGradient(text string, base, peak lipgloss.Style, frame int) string {
	if !shimmerEnabled() {
		return base.Render(text)
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	const radius = 2
	span := len(runes) + 2*radius + 1
	center := frame%span - radius
	if center < -radius {
		center += span
	}
	near := base.Bold(true)
	step := func(index int) int {
		distance := index - center
		if distance < 0 {
			distance = -distance
		}
		switch {
		case distance == 0:
			return 2
		case distance <= radius:
			return 1
		}
		return 0
	}
	styles := [3]lipgloss.Style{base, near, peak}
	var builder strings.Builder
	start := 0
	for index := 1; index <= len(runes); index++ {
		if index == len(runes) || step(index) != step(start) {
			builder.WriteString(styles[step(start)].Render(string(runes[start:index])))
			start = index
		}
	}
	return builder.String()
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
