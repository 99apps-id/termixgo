package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/99apps-id/termixgo/internal/agent"
)

// statsBoxWidth is the inner width of each box in the report.
const statsBoxWidth = 54

// statsArgs are the flags of `termixgo stats`.
type statsArgs struct {
	options agent.StatsOptions
	// tools and models limit the rows shown; zero shows none for models (the
	// section is opt-in) and every tool for tools.
	tools      int
	showModels bool
	models     int
	json       bool
}

func parseStatsArgs(args []string) (statsArgs, error) {
	parsed := statsArgs{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		value := func() (string, bool) {
			if index+1 < len(args) {
				index++
				return args[index], true
			}
			return "", false
		}
		switch arg {
		case "--json":
			parsed.json = true
		case "--days":
			text, ok := value()
			days, err := strconv.Atoi(strings.TrimSpace(text))
			if !ok || err != nil || days < 1 {
				return parsed, fmt.Errorf("--days needs a number of days, for example --days 7")
			}
			parsed.options.Days = days
		case "--tools":
			text, ok := value()
			count, err := strconv.Atoi(strings.TrimSpace(text))
			if !ok || err != nil || count < 1 {
				return parsed, fmt.Errorf("--tools needs how many tools to list, for example --tools 5")
			}
			parsed.tools = count
		case "--models":
			parsed.showModels = true
			// A number after the flag limits the list; anything else is the next flag.
			if index+1 < len(args) {
				if count, err := strconv.Atoi(strings.TrimSpace(args[index+1])); err == nil && count > 0 {
					parsed.models = count
					index++
				}
			}
		case "--project":
			text, ok := value()
			if !ok {
				return parsed, fmt.Errorf("--project needs part of a workspace path, or . for the current folder")
			}
			if strings.TrimSpace(text) == "." {
				if cwd, err := os.Getwd(); err == nil {
					text = cwd
				}
			}
			parsed.options.Project = text
		default:
			return parsed, fmt.Errorf("unknown option %q; usage: termixgo stats [--days N] [--tools N] [--models [N]] [--project <path>|.] [--json]", arg)
		}
	}
	return parsed, nil
}

// runStats prints usage across the saved sessions: how many, what they cost,
// how many tokens they moved, and which tools and models did the work.
func runStats(args []string, stdout io.Writer) error {
	parsed, err := parseStatsArgs(args)
	if err != nil {
		return err
	}
	report, err := agent.SummarizeSessions(parsed.options)
	if err != nil {
		return err
	}
	if parsed.json {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(encoded))
		return nil
	}
	if report.Sessions == 0 {
		fmt.Fprintln(stdout, "No saved sessions match.")
		return nil
	}
	renderStats(stdout, report, parsed)
	return nil
}

func renderStats(out io.Writer, report agent.StatsReport, parsed statsArgs) {
	cost := fmt.Sprintf("$%.2f", report.CostUSD)
	if report.UnpricedRecords > 0 {
		cost = "at least " + cost
	}
	perDay := report.CostUSD / float64(max(report.Days, 1))
	statsBox(out, "OVERVIEW", [][2]string{
		{"Sessions", formatStatsInt(int64(report.Sessions))},
		{"Messages", formatStatsInt(int64(report.Messages))},
		{"Days", formatStatsInt(int64(report.Days))},
	})
	statsBox(out, "COST & TOKENS", [][2]string{
		{"Total Cost", cost},
		{"Avg Cost/Day", fmt.Sprintf("$%.2f", perDay)},
		{"Avg Tokens/Session", formatStatsTokens(report.AvgTokensPerSession)},
		{"Median Tokens/Session", formatStatsTokens(report.MedianTokensPerSession)},
		{"Input", formatStatsTokens(report.InputTokens)},
		{"Output", formatStatsTokens(report.OutputTokens)},
		{"Reasoning", formatStatsTokens(report.ReasoningTokens)},
		{"Cache Read", formatStatsTokens(report.CacheReadTokens)},
		{"Cache Write", formatStatsTokens(report.CacheWriteTokens)},
	})
	if len(report.Tools) > 0 {
		tools := report.Tools
		if parsed.tools > 0 && len(tools) > parsed.tools {
			tools = tools[:parsed.tools]
		}
		var totalCalls int
		for _, tool := range report.Tools {
			totalCalls += tool.Calls
		}
		maxCalls := tools[0].Calls
		rows := make([]string, 0, len(tools))
		for _, tool := range tools {
			barWidth := 16
			filled := 0
			if maxCalls > 0 {
				filled = tool.Calls * barWidth / maxCalls
			}
			if filled < 1 {
				filled = 1
			}
			percent := float64(tool.Calls) * 100 / float64(max(totalCalls, 1))
			rows = append(rows, fmt.Sprintf(" %-14s %s %5d (%4.1f%%)", clipStats(tool.Name, 14), padStats(strings.Repeat("█", filled), barWidth), tool.Calls, percent))
		}
		statsLines(out, "TOOL USAGE", rows)
	}
	if parsed.showModels && len(report.Models) > 0 {
		models := report.Models
		if parsed.models > 0 && len(models) > parsed.models {
			models = models[:parsed.models]
		}
		var rows []string
		for _, model := range models {
			name := model.Model
			if name == "" {
				name = "(unknown)"
			}
			if model.Provider != "" && !strings.HasPrefix(name, model.Provider+"/") {
				name = model.Provider + "/" + name
			}
			// A model with no dollar figure (a subscription or plan) says so instead
			// of printing a price of zero it never had.
			price := fmt.Sprintf("$%.2f", model.CostUSD)
			switch {
			case model.CostUSD == 0 && model.Unpriced > 0:
				price = "unpriced"
			case model.Unpriced > 0:
				price = ">= " + price
			}
			rows = append(rows,
				" "+clipStats(name, statsBoxWidth-2),
				fmt.Sprintf("   %s records   %s tokens   %s", formatStatsInt(int64(model.Records)), formatStatsTokens(int64(model.Usage.TotalTokens)), price))
		}
		statsLines(out, "MODEL USAGE", rows)
	}
}

// statsBox draws a titled box of label and value rows, the value right-aligned.
func statsBox(out io.Writer, title string, rows [][2]string) {
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		gap := statsBoxWidth - utf8.RuneCountInString(row[0]) - utf8.RuneCountInString(row[1])
		if gap < 1 {
			gap = 1
		}
		lines = append(lines, row[0]+strings.Repeat(" ", gap)+row[1])
	}
	statsLines(out, title, lines)
}

// statsLines draws a titled box around ready-made rows.
func statsLines(out io.Writer, title string, lines []string) {
	fmt.Fprintln(out, "┌"+strings.Repeat("─", statsBoxWidth+2)+"┐")
	left := (statsBoxWidth + 2 - utf8.RuneCountInString(title)) / 2
	fmt.Fprintln(out, "│"+strings.Repeat(" ", left)+title+strings.Repeat(" ", statsBoxWidth+2-left-utf8.RuneCountInString(title))+"│")
	fmt.Fprintln(out, "├"+strings.Repeat("─", statsBoxWidth+2)+"┤")
	for _, line := range lines {
		fmt.Fprintln(out, "│ "+padStats(line, statsBoxWidth)+" │")
	}
	fmt.Fprintln(out, "└"+strings.Repeat("─", statsBoxWidth+2)+"┘")
	fmt.Fprintln(out)
}

func padStats(text string, width int) string {
	if gap := width - utf8.RuneCountInString(text); gap > 0 {
		return text + strings.Repeat(" ", gap)
	}
	return clipStats(text, width)
}

func clipStats(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

// formatStatsInt groups thousands: 10557 -> 10,557.
func formatStatsInt(value int64) string {
	text := strconv.FormatInt(value, 10)
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	var grouped []byte
	for index := range text {
		if index > 0 && (len(text)-index)%3 == 0 {
			grouped = append(grouped, ',')
		}
		grouped = append(grouped, text[index])
	}
	if negative {
		return "-" + string(grouped)
	}
	return string(grouped)
}

// formatStatsTokens shortens a token count: 1,234 -> 1.2K, 2,500,000 -> 2.5M.
func formatStatsTokens(value int64) string {
	switch {
	case value >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(value)/1e9)
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1e6)
	case value >= 1_000:
		return fmt.Sprintf("%.1fK", float64(value)/1e3)
	default:
		return strconv.FormatInt(value, 10)
	}
}
