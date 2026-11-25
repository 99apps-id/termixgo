package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/99apps-id/termixgo/internal/app"
)

// quotaTickInterval is how often the strip asks the providers for fresh usage.
// The app skips a provider fetched more recently than its own minimum.
const quotaTickInterval = 3 * time.Minute

// quotaTickMsg asks for a refresh; quotaStripMsg reports that one finished.
type (
	quotaTickMsg  struct{}
	quotaStripMsg struct{}
)

func quotaTick() tea.Cmd {
	return tea.Tick(quotaTickInterval, func(time.Time) tea.Msg { return quotaTickMsg{} })
}

// refreshQuotaStrip fetches usage for every logged-in provider off the UI
// goroutine and then asks for a repaint.
func (m *Model) refreshQuotaStrip(force bool) tea.Cmd {
	if m.app == nil {
		return nil
	}
	application := m.app
	return func() tea.Msg {
		application.RefreshAllQuota(context.Background(), force)
		return quotaStripMsg{}
	}
}

// quotaDetail is how much one row says: each level is shorter than the last, so
// a narrow terminal drops the bar, then the reset time, then the weekly window.
type quotaDetail int

const (
	quotaFull   quotaDetail = iota // bar, 5h left, reset, weekly left and reset
	quotaMedium                    // 5h left with reset, weekly left
	quotaShort                     // 5h left, weekly left
	quotaTiny                      // 5h left only
)

// leftTone picks the colour for what is left: plenty, running low, nearly out.
func (m *Model) leftTone(percent float64) lipgloss.Style {
	switch {
	case percent >= 50:
		return m.styles.StatusTrust
	case percent >= 20:
		return m.styles.StatusWarn
	default:
		return m.styles.Error
	}
}

// formatLeftIn writes a reset countdown the way a person reads it.
func formatLeftIn(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// leftBar draws what remains, not what was used: a full bar is a full
// allowance, and it empties as the allowance is spent.
func leftBar(percent float64, width int) string {
	filled, empty := leftBarParts(percent, width)
	return filled + empty
}

// leftBarParts splits the bar so the spent part can be drawn dim: both halves
// in the same colour made a mostly empty bar read as a full one.
func leftBarParts(percent float64, width int) (filled, empty string) {
	count := int(percent/100*float64(width) + 0.5)
	count = max(0, min(width, count))
	return strings.Repeat("█", count), strings.Repeat("░", width-count)
}

// quotaSegment renders one provider at a level of detail.
func (m *Model) quotaSegment(row app.QuotaRow, detail quotaDetail) string {
	name := m.styles.Dim.Render(row.Label)
	if row.Active {
		name = m.styles.StatusKey.Render(row.Label)
	}
	switch row.State {
	case app.QuotaPending:
		return name + " " + m.styles.Dim.Render("...")
	case app.QuotaUnavailable:
		return name + " " + m.styles.Dim.Render("n/a")
	}

	window := func(left *app.QuotaLeft, tag string, withBar, withReset bool) string {
		tone := m.leftTone(left.LeftPercent)
		text := ""
		if withBar {
			filled, empty := leftBarParts(left.LeftPercent, 8)
			text += tone.Render(filled) + m.styles.Dim.Render(empty) + " "
		}
		text += tone.Render(fmt.Sprintf("%.0f%%", left.LeftPercent))
		if tag != "" {
			text = m.styles.Dim.Render(tag+" ") + text
		}
		text += m.styles.Dim.Render(" left")
		if withReset {
			if in := formatLeftIn(left.ResetsIn); in != "" {
				text += m.styles.Dim.Render(" (" + in + ")")
			}
		}
		return text
	}

	var parts []string
	switch {
	case row.Session != nil:
		parts = append(parts, window(row.Session, "5h", detail == quotaFull, detail <= quotaMedium))
		if row.Weekly != nil && detail <= quotaShort {
			parts = append(parts, window(row.Weekly, "wk", false, detail == quotaFull))
		}
	case row.Weekly != nil:
		parts = append(parts, window(row.Weekly, "wk", detail == quotaFull, detail <= quotaMedium))
	case row.Other != nil:
		tag := strings.ToLower(strings.TrimSpace(row.OtherName))
		if runes := []rune(tag); len(runes) > 12 {
			tag = string(runes[:12])
		}
		parts = append(parts, window(row.Other, tag, detail == quotaFull, detail <= quotaMedium))
	}
	text := name + " " + strings.Join(parts, m.styles.Dim.Render(" · "))
	if row.Stale {
		text += m.styles.Dim.Render(" (old)")
	}
	return text
}

// renderQuotaStrip joins every provider on one line that fits width. It tries
// the fullest level first and drops detail until the line fits; when even the
// shortest does not, it keeps the active provider and counts the rest.
func (m *Model) renderQuotaStrip(rows []app.QuotaRow, width int) string {
	if len(rows) == 0 || width < 12 {
		return ""
	}
	separator := m.styles.Dim.Render("  |  ")
	build := func(detail quotaDetail, items []app.QuotaRow) string {
		segments := make([]string, 0, len(items))
		for _, row := range items {
			segments = append(segments, m.quotaSegment(row, detail))
		}
		return strings.Join(segments, separator)
	}
	for detail := quotaFull; detail <= quotaTiny; detail++ {
		if line := build(detail, rows); lipgloss.Width(line) <= width {
			return line
		}
	}
	// Still too wide: show the active provider first, then as many others as
	// fit, and say how many are hidden.
	ordered := make([]app.QuotaRow, 0, len(rows))
	for _, row := range rows {
		if row.Active {
			ordered = append(ordered, row)
		}
	}
	for _, row := range rows {
		if !row.Active {
			ordered = append(ordered, row)
		}
	}
	for keep := len(ordered) - 1; keep >= 1; keep-- {
		line := build(quotaTiny, ordered[:keep]) + m.styles.Dim.Render(fmt.Sprintf(" +%d", len(ordered)-keep))
		if lipgloss.Width(line) <= width {
			return line
		}
	}
	return truncateLine(build(quotaTiny, ordered[:1]), width)
}

// truncateLine cuts a styled line to width columns.
func truncateLine(line string, width int) string {
	return lipgloss.NewStyle().MaxWidth(width).Render(line)
}

// viewQuota is the usage line above the composer. It is empty, and takes no
// row, until a provider that uses a login has been signed in.
func (m *Model) viewQuota() string {
	if m.app == nil {
		return ""
	}
	return m.renderQuotaStrip(m.app.QuotaRows(), m.renderWidth())
}

// quotaRows is the number of rows the strip takes, for the layouts that count
// them.
func (m *Model) quotaRows() int {
	if m.viewQuota() == "" {
		return 0
	}
	return 1
}
