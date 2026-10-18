package agent

import (
	"fmt"
	"strings"
	"sync"
)

// Todo is one plan item.
type Todo struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // pending | in_progress | completed
	Parent string `json:"parent,omitempty"`
}

// TodoStore holds the current plan. The model writes it through todo_write,
// the UI renders it, and the system prompt carries it so the plan survives
// compaction.
type TodoStore struct {
	mu    sync.Mutex
	items []Todo
}

// NewTodoStore builds an empty store.
func NewTodoStore() *TodoStore { return &TodoStore{} }

// Write replaces the plan, validating statuses and the single active item.
func (s *TodoStore) Write(items []Todo) error {
	normalised := make([]Todo, 0, len(items))
	active := 0
	for index, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(item.Status))
		switch status {
		case "pending", "in_progress", "completed":
		default:
			return fmt.Errorf("todo %d has status %q; use pending, in_progress or completed", index+1, item.Status)
		}
		if status == "in_progress" {
			active++
		}
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = fmt.Sprintf("t%d", index+1)
		}
		normalised = append(normalised, Todo{ID: id, Title: title, Status: status, Parent: item.Parent})
	}
	if active > 1 {
		return fmt.Errorf("only one todo may be in_progress, found %d", active)
	}
	s.mu.Lock()
	s.items = normalised
	s.mu.Unlock()
	return nil
}

// Items returns a copy of the plan.
func (s *TodoStore) Items() []Todo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Todo, len(s.items))
	copy(out, s.items)
	return out
}

// Set replaces the plan without validation, which is how a resumed session
// restores its state.
func (s *TodoStore) Set(items []Todo) {
	s.mu.Lock()
	s.items = append([]Todo{}, items...)
	s.mu.Unlock()
}

// PromptBlock renders the plan for the system prompt, or "" when empty.
func (s *TodoStore) PromptBlock() string {
	items := s.Items()
	if len(items) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("\n\n## CURRENT PLAN\nKeep this list accurate; mark one item in_progress at a time.\n")
	for _, item := range items {
		marker := "[ ]"
		switch item.Status {
		case "in_progress":
			marker = "[>]"
		case "completed":
			marker = "[x]"
		}
		fmt.Fprintf(&builder, "- %s %s\n", marker, item.Title)
	}
	return builder.String()
}

// Progress counts completed and total items.
func (s *TodoStore) Progress() (done, total int) {
	items := s.Items()
	for _, item := range items {
		if item.Status == "completed" {
			done++
		}
	}
	return done, len(items)
}

// Active returns the item the agent is working on, so the status line can name
// the current task instead of only counting the list.
func (s *TodoStore) Active() (Todo, bool) {
	for _, item := range s.Items() {
		if item.Status == "in_progress" {
			return item, true
		}
	}
	return Todo{}, false
}
