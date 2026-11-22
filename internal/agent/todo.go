package agent

import (
	"fmt"
	"strconv"
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
	_, err := s.WriteNoting(items)
	return err
}

// WriteNoting is Write that settles a plan with more than one in_progress item
// instead of refusing it, and says what it changed. A model that starts the
// next item before closing the current one has moved on from it: refusing the
// whole write left the finished item unchecked, so the earlier active items
// are marked completed and the last one stays active.
func (s *TodoStore) WriteNoting(items []Todo) ([]string, error) {
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
		case "done", "complete", "finished":
			status = "completed"
		case "in-progress", "active", "doing":
			status = "in_progress"
		case "todo", "open", "":
			status = "pending"
		default:
			return nil, fmt.Errorf("todo %d has status %q; use pending, in_progress or completed", index+1, item.Status)
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
	var notes []string
	if active > 1 {
		last := -1
		for index := range normalised {
			if normalised[index].Status == "in_progress" {
				last = index
			}
		}
		for index := range normalised {
			if index != last && normalised[index].Status == "in_progress" {
				normalised[index].Status = "completed"
				notes = append(notes, fmt.Sprintf("%s was marked completed because %s is now in progress; set it back to pending if it is not done.", normalised[index].ID, normalised[last].ID))
			}
		}
	}
	s.mu.Lock()
	s.items = normalised
	s.mu.Unlock()
	return notes, nil
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
	builder.WriteString("\n\n## CURRENT PLAN\nMark each item completed with todo_update (by its id) as soon as it is done, and keep one item in_progress at a time.\n")
	for _, item := range items {
		fmt.Fprintf(&builder, "- %s %s: %s\n", todoMarker(item.Status), item.ID, item.Title)
	}
	return builder.String()
}

func todoMarker(status string) string {
	switch status {
	case "in_progress":
		return "[>]"
	case "completed":
		return "[x]"
	}
	return "[ ]"
}

// findTodo resolves how a model names an item: its id in any case, a bare number
// for t<n> or the n-th item, or its title. A miss returns -1.
func findTodo(items []Todo, ref string) int {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return -1
	}
	lower := strings.ToLower(ref)
	for index, item := range items {
		if strings.ToLower(item.ID) == lower {
			return index
		}
	}
	trimmed := strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(lower, "task"), "t"), "-_# ")
	if number, err := strconv.Atoi(trimmed); err == nil {
		for index, item := range items {
			if strings.ToLower(item.ID) == fmt.Sprintf("t%d", number) {
				return index
			}
		}
		if number >= 1 && number <= len(items) {
			return number - 1
		}
	}
	match := -1
	for index, item := range items {
		title := strings.ToLower(item.Title)
		if title == lower {
			return index
		}
		if strings.Contains(title, lower) {
			if match >= 0 {
				return -1
			}
			match = index
		}
	}
	return match
}

// PruneFinished returns nil when every item is completed. A finished plan has
// done its job; keeping it makes the next, unrelated task start under a stale
// "plan 4/4" header and shows the old list in the live view.
func PruneFinished(items []Todo) []Todo {
	if len(items) == 0 {
		return nil
	}
	for _, item := range items {
		if item.Status != "completed" {
			return items
		}
	}
	return nil
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
