package agent

import (
	"context"
	"fmt"
	"strings"
)

// todoUpdateTool updates plan items without rewriting the whole plan. It is the
// cheap way to tick an item off, so it is forgiving about how the item is
// named: a strict id match made the model guess, miss, and leave finished
// items unchecked.
type todoUpdateTool struct{}

func (t *todoUpdateTool) Name() string      { return "todo_update" }
func (t *todoUpdateTool) Aliases() []string { return []string{"update_todo", "set_todo_status"} }
func (t *todoUpdateTool) Mutating() bool    { return false }
func (t *todoUpdateTool) Risk() Risk        { return RiskEdit }
func (t *todoUpdateTool) Description() string {
	return "Update plan items by id (t1, t2, as shown in the plan): mark completed, set in_progress, or retitle. Pass ids to mark several at once. Starting an item while another is in_progress marks the other completed."
}
func (t *todoUpdateTool) Schema() map[string]any {
	return object(map[string]any{
		"id":     strProp("Todo id to update, as shown in the plan (t1). A number or the item's title also works."),
		"ids":    arrayProp("Several todo ids that all get the same status.", strProp("One todo id.")),
		"status": strProp("New status: pending, in_progress, or completed."),
		"title":  strProp("New title text, for a single id."),
	})
}
func (t *todoUpdateTool) Label(a map[string]any) string {
	return "Updating todo " + Shorten(todoRefs(a), 30)
}
func (t *todoUpdateTool) DoneLabel(a map[string]any) string {
	return "Updated todo " + Shorten(todoRefs(a), 30)
}

func todoRefs(args map[string]any) string {
	refs := argList(args, "ids")
	if id := argString(args, "id"); id != "" {
		refs = append([]string{id}, refs...)
	}
	return strings.Join(refs, ", ")
}

func (t *todoUpdateTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	if env.Todos == nil {
		return Result{Output: "todo store unavailable", IsError: true}, nil
	}
	refs := argList(args, "ids")
	if id := strings.TrimSpace(argString(args, "id")); id != "" {
		refs = append([]string{id}, refs...)
	}
	status := strings.ToLower(strings.TrimSpace(argString(args, "status")))
	title := strings.TrimSpace(argString(args, "title"))
	if len(refs) == 0 {
		// A title alone names the item to change when no id is given.
		if title == "" {
			return Result{Output: "id is required (the plan shows each item's id, for example t1)", IsError: true}, nil
		}
		refs = []string{title}
		title = ""
	}
	if status == "" && title == "" {
		return Result{Output: "pass a status (pending, in_progress, completed) or a new title", IsError: true}, nil
	}

	items := env.Todos.Items()
	if len(items) == 0 {
		return Result{Output: "The plan is empty; create it with todo_write.", IsError: true}, nil
	}
	var missing []string
	activated := -1
	for _, ref := range refs {
		index := findTodo(items, ref)
		if index < 0 {
			missing = append(missing, ref)
			continue
		}
		if status != "" {
			items[index].Status = status
			if normaliseTodoStatus(status) == "in_progress" {
				activated = index
			}
		}
		if title != "" && len(refs) == 1 {
			items[index].Title = title
		}
	}
	if len(missing) == len(refs) {
		return Result{Output: fmt.Sprintf("No plan item matches %s.%s", strings.Join(missing, ", "), env.Todos.PromptBlock()), IsError: true}, nil
	}
	// Starting an item moves on from the active one. Settling it here keeps
	// the plan valid and the finished item ticked, and the note says so.
	var notes []string
	if activated >= 0 {
		for index := range items {
			if index != activated && normaliseTodoStatus(items[index].Status) == "in_progress" {
				items[index].Status = "completed"
				notes = append(notes, fmt.Sprintf("%s was marked completed because %s is now in progress; set it back to pending if it is not done.", items[index].ID, items[activated].ID))
			}
		}
	}
	more, err := env.Todos.WriteNoting(items)
	notes = append(notes, more...)
	if err != nil {
		return Result{Output: fmt.Sprintf("write failed: %v", err), IsError: true}, nil
	}
	return todoUpdateResult(env, missing, notes), nil
}

func normaliseTodoStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "in_progress", "in-progress", "active", "doing":
		return "in_progress"
	case "completed", "done", "complete", "finished":
		return "completed"
	}
	return "pending"
}

func todoUpdateResult(env *Env, missing, notes []string) Result {
	done, total := env.Todos.Progress()
	output := fmt.Sprintf("Todo updated: %d of %d complete.", done, total)
	for _, note := range notes {
		output += "\n" + note
	}
	if len(missing) > 0 {
		output += "\nNo plan item matches " + strings.Join(missing, ", ") + "."
	}
	return Result{Output: output + env.Todos.PromptBlock(), Plan: env.Todos.Items()}
}
