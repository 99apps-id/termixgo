package agent

import (
	"context"
	"strings"
	"testing"
)

func todoEnv(t *testing.T, items ...Todo) *Env {
	t.Helper()
	env := testEnv(t)
	env.Todos = NewTodoStore()
	if err := env.Todos.Write(items); err != nil {
		t.Fatal(err)
	}
	return env
}

func statuses(store *TodoStore) string {
	var out []string
	for _, item := range store.Items() {
		out = append(out, item.ID+"="+item.Status)
	}
	return strings.Join(out, " ")
}

// The plan the model reads names each item's id, which is what todo_update
// needs; without it the model guessed ids and finished items stayed open.
func TestPlanShowsIDs(t *testing.T) {
	env := todoEnv(t, Todo{Title: "Add flag", Status: "in_progress"}, Todo{Title: "Test it", Status: "pending"})
	block := env.Todos.PromptBlock()
	if !strings.Contains(block, "[>] t1: Add flag") || !strings.Contains(block, "[ ] t2: Test it") {
		t.Fatalf("block = %q", block)
	}
}

func TestTodoUpdateFindsItemsTheWayModelsNameThem(t *testing.T) {
	for _, ref := range []string{"t1", "T1", "1", "task-1", "Add flag", "add fl"} {
		env := todoEnv(t, Todo{Title: "Add flag", Status: "in_progress"}, Todo{Title: "Test it", Status: "pending"})
		result, _ := (&todoUpdateTool{}).Run(context.Background(), env, map[string]any{"id": ref, "status": "completed"})
		if result.IsError || statuses(env.Todos) != "t1=completed t2=pending" {
			t.Fatalf("ref %q: %s / %s", ref, statuses(env.Todos), result.Output)
		}
	}
}

func TestTodoUpdateTicksSeveralAtOnce(t *testing.T) {
	env := todoEnv(t, Todo{Title: "a", Status: "completed"}, Todo{Title: "b", Status: "in_progress"}, Todo{Title: "c", Status: "pending"})
	result, _ := (&todoUpdateTool{}).Run(context.Background(), env, map[string]any{"ids": []any{"t2", "t3"}, "status": "done"})
	if result.IsError || statuses(env.Todos) != "t1=completed t2=completed t3=completed" || !strings.Contains(result.Output, "3 of 3") {
		t.Fatalf("%s / %s", statuses(env.Todos), result.Output)
	}
	if len(result.Plan) != 3 {
		t.Fatalf("the result must carry the plan for the UI")
	}
}

// Starting the next item while the current one is in progress used to fail
// the whole update, so the finished item was never ticked.
func TestStartingTheNextItemCompletesTheCurrentOne(t *testing.T) {
	env := todoEnv(t, Todo{Title: "a", Status: "in_progress"}, Todo{Title: "b", Status: "pending"})
	result, _ := (&todoUpdateTool{}).Run(context.Background(), env, map[string]any{"id": "t2", "status": "in_progress"})
	if result.IsError || statuses(env.Todos) != "t1=completed t2=in_progress" || !strings.Contains(result.Output, "t1 was marked completed") {
		t.Fatalf("%s / %s", statuses(env.Todos), result.Output)
	}
}

func TestTodoWriteSettlesTwoActiveItems(t *testing.T) {
	env := todoEnv(t)
	result, _ := (&todoWriteTool{}).Run(context.Background(), env, map[string]any{"todos": []any{
		map[string]any{"title": "a", "status": "in_progress"},
		map[string]any{"title": "b", "status": "in_progress"},
		map[string]any{"title": "c", "status": "todo"},
	}})
	if result.IsError || statuses(env.Todos) != "t1=completed t2=in_progress t3=pending" {
		t.Fatalf("%s / %s", statuses(env.Todos), result.Output)
	}
}

func TestTodoUpdateMissNamesThePlan(t *testing.T) {
	env := todoEnv(t, Todo{Title: "a", Status: "pending"})
	result, _ := (&todoUpdateTool{}).Run(context.Background(), env, map[string]any{"id": "t9", "status": "completed"})
	if !result.IsError || !strings.Contains(result.Output, "t1: a") {
		t.Fatalf("a miss should show the ids that exist: %s", result.Output)
	}
}

func TestCriticNudgeNamesIDsAndTheTool(t *testing.T) {
	verdict := ReviewFinish([]Todo{{ID: "t2", Title: "Test it", Status: "pending"}}, VerifyLedger{}, false, 0)
	if !verdict.BlockFinish || !strings.Contains(verdict.Nudge, "t2: Test it") || !strings.Contains(verdict.Nudge, "todo_update") {
		t.Fatalf("nudge = %q", verdict.Nudge)
	}
}

func TestTodoUpdateIsAlwaysOffered(t *testing.T) {
	if !toolSearchAlwaysOn["todo_update"] {
		t.Fatalf("with tool search on the model could not tick items off")
	}
}
