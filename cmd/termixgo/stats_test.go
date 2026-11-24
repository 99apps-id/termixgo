package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

func TestParseStatsArgs(t *testing.T) {
	parsed, err := parseStatsArgs([]string{"--days", "7", "--tools", "5", "--models", "3", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.options.Days != 7 || parsed.tools != 5 || !parsed.showModels || parsed.models != 3 || !parsed.json {
		t.Fatalf("parsed = %+v", parsed)
	}
	// --models alone lists every model; a following flag is not its count.
	parsed, err = parseStatsArgs([]string{"--models", "--json"})
	if err != nil || !parsed.showModels || parsed.models != 0 || !parsed.json {
		t.Fatalf("parsed = %+v err=%v", parsed, err)
	}
	for _, bad := range [][]string{{"--days"}, {"--days", "x"}, {"--days", "0"}, {"--tools", "-1"}, {"--project"}, {"--nope"}} {
		if _, err := parseStatsArgs(bad); err == nil {
			t.Errorf("%v should be refused", bad)
		}
	}
}

func TestRenderStatsFitsItsBoxes(t *testing.T) {
	report := agent.StatsReport{
		Sessions: 147, Messages: 10557, Days: 175, CostUSD: 15.18,
		InputTokens: 71_000_000, OutputTokens: 3_800_000, CacheReadTokens: 2_583_900_000, CacheWriteTokens: 830_200,
		AvgTokensPerSession: 18_100_000, MedianTokensPerSession: 598_900,
		Tools:  []agent.ToolUse{{Name: "read_file", Calls: 4114}, {Name: "run_command", Calls: 3446}, {Name: "a_tool_with_a_very_long_name", Calls: 1}},
		Models: []agent.ModelUse{{Provider: "openai", Model: "openai/gpt-6-sol", Records: 12, CostUSD: 1.5}, {Model: "plan-model", Records: 3, Unpriced: 3}},
	}
	var out bytes.Buffer
	renderStats(&out, report, statsArgs{showModels: true})
	text := out.String()
	for _, want := range []string{"OVERVIEW", "10,557", "COST & TOKENS", "$15.18", "2.6B", "TOOL USAGE", "read_file", "MODEL USAGE", "openai/gpt-6-sol", "unpriced"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "openai/openai/") {
		t.Errorf("the provider must not be written twice:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		if width := len([]rune(line)); width != statsBoxWidth+4 {
			t.Errorf("a box row is %d columns, want %d: %q", width, statsBoxWidth+4, line)
		}
	}
}

func TestFormatStats(t *testing.T) {
	cases := map[int64]string{999: "999", 1234: "1.2K", 2_500_000: "2.5M", 3_900_000_000: "3.9B"}
	for value, want := range cases {
		if got := formatStatsTokens(value); got != want {
			t.Errorf("formatStatsTokens(%d) = %q, want %q", value, got, want)
		}
	}
	if got := formatStatsInt(10557); got != "10,557" {
		t.Errorf("formatStatsInt = %q", got)
	}
}
