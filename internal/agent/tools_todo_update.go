package agent

import (
	"context"
	"fmt"
	"strings"
)

// todoUpdateTool updates individual todo items without rewriting the whole plan.
type todoUpdateTool struct{}

func (t *todoUpdateTool) Name() string      { return "todo_update" }
func (t *todoUpdateTool) Aliases() []string { return []string{"update_todo", "set_todo_status"} }
func (t *todoUpdateTool) Mutating() bool    { return false }
func (t *todoUpdateTool) Risk() Risk        { return RiskEdit }
func (t *todoUpdateTool) Description() string {
	return "Update one todo item by id: mark complete, set in_progress, or retitle."
}
func (t *todoUpdateTool) Schema() map[string]any {
	return object(map[string]any{
		"id":     strProp("Todo id to update."),
		"status": strProp("New status: pending, in_progress, or completed."),
		"title":  strProp("New title text."),
	})
}
func (t *todoUpdateTool) Label(a map[string]any) string {
	return "Updating todo " + Shorten(argString(a, "id"), 20)
}
func (t *todoUpdateTool) DoneLabel(a map[string]any) string {
	return "Updated todo " + Shorten(argString(a, "id"), 20)
}

func (t *todoUpdateTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	id := strings.TrimSpace(argString(args, "id"))
	status := strings.ToLower(strings.TrimSpace(argString(args, "status")))
	title := strings.TrimSpace(argString(args, "title"))
	if id == "" && title == "" {
		return Result{Output: "id or title is required", IsError: true}, nil
	}

	items := env.Todos.Items()
	found := false
	updated := false
	for i, item := range items {
		if id != "" && item.ID == id {
			found = true
			if status != "" {
				switch status {
				case "pending", "in_progress", "completed":
					items[i].Status = status
					updated = true
				default:
					return Result{Output: fmt.Sprintf("invalid status %q; use pending, in_progress, or completed", status), IsError: true}, nil
				}
			}
			if title != "" {
				items[i].Title = title
				updated = true
			}
			break
		}
	}
	if !found {
		return Result{Output: "todo not found", IsError: true}, nil
	}
	if !updated {
		return Result{Output: "no changes", IsError: true}, nil
	}
	if err := env.Todos.Write(items); err != nil {
		return Result{Output: fmt.Sprintf("write failed: %v", err), IsError: true}, nil
	}
	return Result{Output: "Todo updated"}, nil
}
