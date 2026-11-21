package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/99apps-id/termixgo/internal/app"
	"github.com/99apps-id/termixgo/internal/oauth"
	"github.com/99apps-id/termixgo/internal/provider"
)

func quotaRowsFixture() []app.QuotaRow {
	return []app.QuotaRow{
		{Provider: "claude-oauth", Label: "Claude", Active: true, State: app.QuotaKnown,
			Session: &app.QuotaLeft{LeftPercent: 72, ResetsIn: 2*time.Hour + 14*time.Minute},
			Weekly:  &app.QuotaLeft{LeftPercent: 41, ResetsIn: 3*24*time.Hour + 5*time.Hour}},
		{Provider: "openai-codex", Label: "Codex", State: app.QuotaKnown,
			Session: &app.QuotaLeft{LeftPercent: 90, ResetsIn: 4*time.Hour + 40*time.Minute}},
		{Provider: "antigravity", Label: "Antigravity", State: app.QuotaPending},
	}
}

func TestQuotaStripSaysWhatIsLeftForEveryProvider(t *testing.T) {
	model := chatModel(t)
	line := stripANSI(model.renderQuotaStrip(quotaRowsFixture(), 160))
	for _, want := range []string{"left", "Claude", "72% left (2h14m)", "wk 41% left (3d5h)", "Codex", "90% left (4h40m)", "Antigravity ..."} {
		if !strings.Contains(line, want) {
			t.Fatalf("strip lacks %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "used") {
		t.Fatalf("the strip shows what is left, never what was used:\n%s", line)
	}
}

// A narrower terminal drops the bar, then the resets, then the weekly window,
// and never wraps: a wrapped line would push the frame down.
func TestQuotaStripNeverExceedsTheWidth(t *testing.T) {
	model := chatModel(t)
	for _, width := range []int{200, 120, 100, 80, 60, 40, 30, 20, 14} {
		line := model.renderQuotaStrip(quotaRowsFixture(), width)
		if line == "" {
			t.Fatalf("width %d: the strip vanished", width)
		}
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("width %d: strip is %d wide:\n%s", width, got, stripANSI(line))
		}
		if strings.Contains(line, "\n") {
			t.Fatalf("width %d: the strip must be one line", width)
		}
	}
	narrow := stripANSI(model.renderQuotaStrip(quotaRowsFixture(), 30))
	if !strings.Contains(narrow, "Claude") || !strings.Contains(narrow, "+") {
		t.Fatalf("a narrow strip keeps the active provider and counts the rest:\n%s", narrow)
	}
}

func TestQuotaStripIsEmptyWithoutAnyLogin(t *testing.T) {
	model := chatModel(t)
	if got := model.renderQuotaStrip(nil, 120); got != "" {
		t.Fatalf("strip = %q", got)
	}
	if model.quotaRows() != 0 {
		t.Fatalf("no login, no row reserved")
	}
}

func TestLeftBarAndCountdown(t *testing.T) {
	if got := leftBar(100, 8); got != "████████" {
		t.Fatalf("full = %q", got)
	}
	if got := leftBar(0, 8); got != "░░░░░░░░" {
		t.Fatalf("empty = %q", got)
	}
	if got := leftBar(50, 8); got != "████░░░░" {
		t.Fatalf("half = %q", got)
	}
	for in, want := range map[time.Duration]string{
		30 * time.Second:             "<1m",
		12 * time.Minute:             "12m",
		2*time.Hour + 5*time.Minute:  "2h05m",
		3*24*time.Hour + 5*time.Hour: "3d5h",
		0:                            "",
	} {
		if got := formatLeftIn(in); got != want {
			t.Fatalf("formatLeftIn(%v) = %q, want %q", in, got, want)
		}
	}
}

// With a login and an official snapshot the strip sits above the composer, and
// the old displays (the header gauge, the local 5h count) are gone.
func TestQuotaStripReplacesTheOldDisplays(t *testing.T) {
	model := chatModel(t)
	if err := provider.OAuthStore(model.app.Secrets()).Save("claude-oauth", oauth.Token{Access: "demo"}); err != nil {
		t.Fatalf("login: %v", err)
	}
	reset := time.Now().Add(2 * time.Hour)
	model.app.QuotaTracker().RecordSnapshot("claude-oauth", &provider.QuotaSnapshot{
		Provider: "claude-oauth", FetchedAt: time.Now(),
		Windows: []provider.QuotaWindow{{Key: provider.QuotaSessionKey, UsedPercent: 30, ResetsAt: &reset}},
	})
	model.layout()
	view := stripANSI(model.View())
	if !strings.Contains(view, "Claude") || !strings.Contains(view, "70% left") {
		t.Fatalf("the strip should show above the composer:\n%s", view)
	}
	if strings.Contains(view, "local estimate") || strings.Contains(view, "5h:") || strings.Contains(view, "[█") {
		t.Fatalf("the old quota displays must be gone:\n%s", view)
	}
	rows := strings.Split(view, "\n")
	strip, composer := -1, -1
	for i, row := range rows {
		if strings.Contains(row, "70% left") && strip < 0 {
			strip = i
		}
		if strings.Contains(row, "Ask Termixgo") || strings.Contains(row, ">") && composer < 0 && strip >= 0 {
			composer = i
		}
	}
	if strip < 0 || composer <= strip {
		t.Fatalf("the strip belongs above the composer (strip row %d, composer row %d):\n%s", strip, composer, view)
	}
}

func TestQuotaStripMarksStaleFigures(t *testing.T) {
	model := chatModel(t)
	rows := quotaRowsFixture()
	rows[0].Stale = true
	line := stripANSI(model.renderQuotaStrip(rows, 200))
	if !strings.Contains(line, "(old)") {
		t.Fatalf("figures from failing refreshes must say so:\n%s", line)
	}
}
