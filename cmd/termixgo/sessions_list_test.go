package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
)

func TestSessionLineShowsCostTokensAndChanges(t *testing.T) {
	line := sessionLine(agent.SessionSummary{
		ID: "abc", Title: "Fix the parser", Turns: 7,
		UpdatedAt: time.Date(2026, 11, 24, 18, 49, 0, 0, time.UTC),
		CostUSD:   0.4271, Tokens: 1_234_000,
		Changes: agent.SessionChanges{Files: 3, Added: 45, Removed: 12},
	})
	for _, want := range []string{"abc", "2026-11-24 18:49", "Fix the parser (7 turns)", "$0.43", "1.2M tok", "3 files +45 -12"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line lacks %q: %s", want, line)
		}
	}
}

func TestSessionLineSaysUnpricedAndEmpty(t *testing.T) {
	plan := sessionLine(agent.SessionSummary{ID: "p", Tokens: 500, Changes: agent.SessionChanges{Files: 1, Added: 2}})
	if !strings.Contains(plan, "unpriced") || !strings.Contains(plan, "1 file +2 -0") || !strings.Contains(plan, "(untitled)") {
		t.Errorf("plan session line = %s", plan)
	}
	empty := sessionLine(agent.SessionSummary{ID: "e"})
	if !strings.Contains(empty, "no file changes") || strings.Contains(empty, "unpriced") || strings.Contains(empty, "$") {
		t.Errorf("empty session line = %s", empty)
	}
}

func TestListSessionsFlags(t *testing.T) {
	withState(t)
	var out bytes.Buffer
	if err := listSessions([]string{"--bogus"}, &out); err == nil {
		t.Errorf("an unknown option must be refused")
	}
	if err := listSessions([]string{"--limit", "0"}, &out); err == nil {
		t.Errorf("a zero limit must be refused")
	}
	out.Reset()
	if err := listSessions(nil, &out); err != nil || !strings.Contains(out.String(), "No saved sessions") {
		t.Errorf("an empty store: err=%v out=%q", err, out.String())
	}
}
