package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

// titleCap keeps a renamed session readable in the picker list.
const titleCap = 120

// SetTitle renames the live session. A blank title clears it back to
// untitled rather than storing whitespace.
func (s *Session) SetTitle(title string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	trimmed := strings.TrimSpace(title)
	if len(trimmed) > titleCap {
		trimmed = trimmed[:titleCap]
	}
	s.title = trimmed
	s.updatedAt = time.Now()
}

// RenameSession changes a saved session's title.
func RenameSession(id, title string) (SessionSummary, error) {
	if strings.TrimSpace(title) == "" {
		return SessionSummary{}, fmt.Errorf("title is required")
	}
	session, err := LoadSession(id)
	if err != nil {
		return SessionSummary{}, err
	}
	session.SetTitle(title)
	if err := session.Save(); err != nil {
		return SessionSummary{}, err
	}
	return SessionSummary{ID: session.ID(), Title: session.Title(), Model: session.Model(), UpdatedAt: session.UpdatedAt(), Turns: session.Turns()}, nil
}

// DeleteSession removes one saved session file. The id is confined to the
// sessions directory: separators are refused rather than joined, so a crafted
// id cannot reach outside it.
func DeleteSession(id string) error {
	if err := checkSessionID(id); err != nil {
		return err
	}
	dir, err := config.SessionsDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, id+".json")
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("session %s not found", shortSessionID(id))
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// SearchSessions filters the saved sessions by title or id, newest first.
func SearchSessions(query string) ([]SessionSummary, error) {
	summaries, err := ListSessions()
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return summaries, nil
	}
	var matches []SessionSummary
	for _, summary := range summaries {
		if strings.Contains(strings.ToLower(summary.Title), needle) ||
			strings.Contains(strings.ToLower(summary.ID), needle) {
			matches = append(matches, summary)
		}
	}
	return matches, nil
}

// ExportSession renders one saved session as Markdown for sharing or review.
func ExportSession(id string) (string, error) {
	session, err := LoadSession(id)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	title := strings.TrimSpace(session.Title())
	if title == "" {
		title = "Untitled session"
	}
	fmt.Fprintf(&builder, "# %s\n\n", title)
	fmt.Fprintf(&builder, "Session `%s` - %d turn(s) - %s\n\n",
		session.ID(), session.Turns(), session.UpdatedAt().Format("2006-01-02 15:04"))
	for _, message := range session.Messages() {
		role := strings.ToLower(strings.TrimSpace(string(message.Role)))
		if role == "" {
			role = "unknown"
		}
		fmt.Fprintf(&builder, "## %s\n\n", role)
		if strings.TrimSpace(message.Content) != "" {
			builder.WriteString(strings.TrimSpace(message.Content) + "\n\n")
		}
		for _, call := range message.ToolCalls {
			fmt.Fprintf(&builder, "- tool `%s`\n", call.Name)
		}
	}
	return strings.TrimSpace(builder.String()) + "\n", nil
}

func checkSessionID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("session id is required")
	}
	if strings.ContainsAny(id, `/\.`) {
		return fmt.Errorf("invalid session id %q", id)
	}
	return nil
}

func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
