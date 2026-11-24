package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeStatsSession(t *testing.T, dir, id, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func statsSessionJSON(workspace string, updated time.Time, prompt, completion, cacheRead int, cost float64, tools ...string) string {
	var calls []string
	for _, name := range tools {
		calls = append(calls, fmt.Sprintf(`{"ID":"c","Name":%q,"Arguments":"{}"}`, name))
	}
	return fmt.Sprintf(`{"id":"x","workspace":%q,"createdAt":%q,"updatedAt":%q,
	"messages":[{"Role":"user","Content":"hi"},{"Role":"assistant","Content":"","ToolCalls":[%s]},{"Role":"tool","Content":"ok"}],
	"usage":{"PromptTokens":%d,"CompletionTokens":%d,"TotalTokens":%d,"CacheReadTokens":%d},
	"costUsd":%v,
	"segments":[{"provider":"openai","model":"gpt-6-sol","billing_mode":"api","usage":{"PromptTokens":%d,"CompletionTokens":%d,"TotalTokens":%d,"CacheReadTokens":%d},"costUsd":%v,"records":2}]}`,
		workspace, updated.Add(-time.Hour).Format(time.RFC3339), updated.Format(time.RFC3339), strings.Join(calls, ","),
		prompt, completion, prompt+completion, cacheRead, cost,
		prompt, completion, prompt+completion, cacheRead, cost)
}

func TestSummarizeSessionsRollsUpUsageAndTools(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	writeStatsSession(t, dir, "a", statsSessionJSON("C:/Work/App", now, 1000, 100, 600, 0.50, "read_file", "read_file", "grep"))
	writeStatsSession(t, dir, "b", statsSessionJSON("C:/Work/Other", now.AddDate(0, 0, -2), 400, 50, 0, 0.25, "run_command"))

	report, err := SummarizeSessions(StatsOptions{Dir: dir, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if report.Sessions != 2 || report.Messages != 6 {
		t.Fatalf("sessions=%d messages=%d, want 2 and 6", report.Sessions, report.Messages)
	}
	if report.CostUSD != 0.75 {
		t.Fatalf("cost = %v, want 0.75", report.CostUSD)
	}
	// Input is the prompt minus what the cache served: (1000-600) + 400.
	if report.InputTokens != 800 || report.OutputTokens != 150 || report.CacheReadTokens != 600 {
		t.Fatalf("input=%d output=%d cacheRead=%d", report.InputTokens, report.OutputTokens, report.CacheReadTokens)
	}
	// Per session: 400+100+600 = 1100 and 400+50 = 450.
	if report.AvgTokensPerSession != 775 || report.MedianTokensPerSession != 775 {
		t.Fatalf("avg=%d median=%d, want 775 and 775", report.AvgTokensPerSession, report.MedianTokensPerSession)
	}
	if len(report.Tools) != 3 || report.Tools[0].Name != "read_file" || report.Tools[0].Calls != 2 {
		t.Fatalf("tools = %+v", report.Tools)
	}
	if len(report.Models) != 1 || report.Models[0].Model != "gpt-6-sol" || report.Models[0].Records != 4 {
		t.Fatalf("models = %+v", report.Models)
	}
	if report.Days < 2 {
		t.Fatalf("days = %d, want the span of the two sessions", report.Days)
	}
}

func TestSummarizeSessionsFiltersByDaysAndProject(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	writeStatsSession(t, dir, "new", statsSessionJSON(`C:\Work\App`, now, 100, 10, 0, 0.1, "grep"))
	writeStatsSession(t, dir, "old", statsSessionJSON(`C:\Work\Other`, now.AddDate(0, 0, -30), 100, 10, 0, 0.1, "grep"))

	recent, _ := SummarizeSessions(StatsOptions{Dir: dir, Now: now, Days: 7})
	if recent.Sessions != 1 {
		t.Fatalf("a 7 day window kept %d sessions, want 1", recent.Sessions)
	}
	// The project filter ignores case and slash direction.
	app, _ := SummarizeSessions(StatsOptions{Dir: dir, Now: now, Project: "c:/work/app"})
	if app.Sessions != 1 {
		t.Fatalf("project filter kept %d sessions, want 1", app.Sessions)
	}
	none, _ := SummarizeSessions(StatsOptions{Dir: dir, Now: now, Project: "missing"})
	if none.Sessions != 0 {
		t.Fatalf("a project nothing matches kept %d sessions", none.Sessions)
	}
}

// A damaged file and a non-session file are skipped, the rest still counts.
func TestSummarizeSessionsSkipsWhatItCannotRead(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	writeStatsSession(t, dir, "good", statsSessionJSON("w", now, 10, 1, 0, 0, "grep"))
	writeStatsSession(t, dir, "bad", "{not json")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := SummarizeSessions(StatsOptions{Dir: dir, Now: now})
	if err != nil || report.Sessions != 1 {
		t.Fatalf("sessions=%d err=%v, want the one readable session", report.Sessions, err)
	}
}

func TestSummarizeSessionsOfAnEmptyFolder(t *testing.T) {
	report, err := SummarizeSessions(StatsOptions{Dir: t.TempDir()})
	if err != nil || report.Sessions != 0 || len(report.Tools) != 0 {
		t.Fatalf("report=%+v err=%v, want an empty report", report, err)
	}
}

// A session with no recorded tokens does not drag the median to zero.
func TestEmptySessionsDoNotSkewTheAverage(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	writeStatsSession(t, dir, "empty", statsSessionJSON("w", now, 0, 0, 0, 0))
	writeStatsSession(t, dir, "used", statsSessionJSON("w", now, 1000, 0, 0, 0))
	report, _ := SummarizeSessions(StatsOptions{Dir: dir, Now: now})
	if report.Sessions != 2 || report.AvgTokensPerSession != 1000 || report.MedianTokensPerSession != 1000 {
		t.Fatalf("sessions=%d avg=%d median=%d, want 2, 1000 and 1000", report.Sessions, report.AvgTokensPerSession, report.MedianTokensPerSession)
	}
}
