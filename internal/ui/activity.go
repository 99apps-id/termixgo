package ui

import (
	"fmt"
	"strings"
	"time"
)

// The activity line
//
// While a turn runs, one line above the status bar says what the agent is
// doing, for how long, how much it has written, and how to stop it:
//
//	✻ Thinking… (14s · ↓ 2.1k tokens · esc to interrupt)
//
// It replaces the running "Thinking..." header in the live transcript and the
// working indicator in the hint row, which said the same thing in two places.

// activityGlyphs is the spinner, one glyph per animation frame.
var activityGlyphs = []string{"·", "✢", "✳", "✶", "✻", "✽", "✻", "✶", "✳", "✢"}

// activityFramePeriod is how long one spinner glyph stays.
const activityFramePeriod = 120 * time.Millisecond

func activityGlyph(now time.Time) string {
	frame := int(now.UnixMilli()/activityFramePeriod.Milliseconds()) % len(activityGlyphs)
	return activityGlyphs[frame]
}

// activityVerb names what the newest unfinished block is doing.
func (m *Model) activityVerb() string {
	for index := len(m.blocks) - 1; index >= 0; index-- {
		item := m.blocks[index]
		switch {
		case item.kind == blockTool && item.running:
			label := strings.TrimSpace(item.toolLabel)
			if label == "" {
				label = "Running " + item.toolName
			}
			return strings.TrimSuffix(label, "...")
		case item.kind == blockThinking && item.running:
			return "Thinking"
		case item.kind == blockAssistant:
			return "Writing"
		case item.kind == blockUser:
			return "Working"
		}
	}
	return "Working"
}

// activityLine renders the line, or "" when no turn runs.
func (m *Model) activityLine() string {
	if !m.running {
		return ""
	}
	details := []string{formatElapsed(time.Since(m.runStarted))}
	if tokens := m.turnStats.streamed / 4; tokens > 0 {
		details = append(details, "↓ "+shortCount(tokens)+" tokens")
	}
	if count := m.queuedCount(); count > 0 {
		details = append(details, fmt.Sprintf("%d queued", count))
	}
	details = append(details, "esc to interrupt")
	verb := m.activityVerb() + "…"
	line := m.styles.Plan.Render(activityGlyph(time.Now())) + " " +
		shimmerWith(verb, m.styles.Plan, shimmerFrame()) + " " +
		m.styles.Dim.Render("("+strings.Join(details, " · ")+")")
	return truncateANSI(line, m.renderWidth())
}

// liveWithoutRunningThinking drops a running thinking block from the compact
// live area: the activity line already says the agent is thinking, and a
// second "Thinking..." just above it only repeated it.
func liveWithoutRunningThinking(items []block, showDetails bool) []block {
	if showDetails {
		return items
	}
	out := items[:0:0]
	for _, item := range items {
		if item.kind == blockThinking && item.running {
			continue
		}
		out = append(out, item)
	}
	return out
}
