package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/99apps-id/termixgo/internal/agent"
)

// listSessions prints the saved sessions with what each one cost and changed.
func listSessions(args []string, stdout io.Writer) error {
	asJSON := false
	limit := 0
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--json":
			asJSON = true
		case "--limit", "-n":
			if index+1 >= len(args) {
				return fmt.Errorf("--limit needs a number, for example --limit 10")
			}
			index++
			count, err := strconv.Atoi(strings.TrimSpace(args[index]))
			if err != nil || count < 1 {
				return fmt.Errorf("--limit needs a number, for example --limit 10")
			}
			limit = count
		default:
			return fmt.Errorf("unknown option %q; usage: termixgo sessions list [--limit N] [--json]", args[index])
		}
	}
	sessions, err := agent.ListSessions()
	if err != nil {
		return err
	}
	total := len(sessions)
	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	if asJSON {
		encoded, err := json.MarshalIndent(sessions, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(encoded))
		return nil
	}
	if total == 0 {
		fmt.Fprintln(stdout, "No saved sessions yet.")
		return nil
	}
	if len(sessions) < total {
		fmt.Fprintf(stdout, "Saved sessions (%d of %d):\n", len(sessions), total)
	} else {
		fmt.Fprintf(stdout, "Saved sessions (%d):\n", total)
	}
	for _, session := range sessions {
		fmt.Fprintln(stdout, sessionLine(session))
	}
	return nil
}

// sessionLine is one row of a listing: the identity of the session, then its
// cost, its tokens and the files it changed.
func sessionLine(s agent.SessionSummary) string {
	title := s.Title
	if title == "" {
		title = "(untitled)"
	}
	return fmt.Sprintf("  %s  %-16s  %s (%d turns)  %s  %s  %s",
		s.ID, s.UpdatedAt.Format("2006-01-02 15:04"), title, s.Turns,
		sessionCost(s), sessionTokens(s), sessionChanges(s.Changes))
}

// sessionCost shows a dollar figure when there is one. A session that moved
// tokens without a price (a subscription or plan model) says so instead of
// claiming it was free.
func sessionCost(s agent.SessionSummary) string {
	switch {
	case s.CostUSD > 0:
		return fmt.Sprintf("$%.2f", s.CostUSD)
	case s.Tokens > 0:
		return "unpriced"
	default:
		return "-"
	}
}

func sessionTokens(s agent.SessionSummary) string {
	if s.Tokens <= 0 {
		return "-"
	}
	return formatStatsTokens(int64(s.Tokens)) + " tok"
}

func sessionChanges(c agent.SessionChanges) string {
	if c.Files == 0 {
		return "no file changes"
	}
	noun := "files"
	if c.Files == 1 {
		noun = "file"
	}
	return fmt.Sprintf("%d %s +%d -%d", c.Files, noun, c.Added, c.Removed)
}
