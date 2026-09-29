package agent

import "testing"

// TestTodoStoreActiveNamesTheWorkInFlight keeps the plan and the status line in
// step: the store has to be able to answer "what is the agent on right now".
func TestTodoStoreActiveNamesTheWorkInFlight(t *testing.T) {
	store := NewTodoStore()
	if _, ok := store.Active(); ok {
		t.Fatalf("an empty plan has no active item")
	}

	if err := store.Write([]Todo{
		{ID: "t1", Title: "Split the tokenizer", Status: "completed"},
		{ID: "t2", Title: "Wire the parser", Status: "in_progress"},
		{ID: "t3", Title: "Add tests", Status: "pending"},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	active, ok := store.Active()
	if !ok {
		t.Fatalf("an item is in progress, so Active should find it")
	}
	if active.ID != "t2" || active.Title != "Wire the parser" {
		t.Errorf("Active = %+v, want the in-progress item", active)
	}
}

// TestTodoStoreActiveIsEmptyWhenEverythingIsDone stops a finished plan from
// leaving a stale task on screen.
func TestTodoStoreActiveIsEmptyWhenEverythingIsDone(t *testing.T) {
	store := NewTodoStore()
	if err := store.Write([]Todo{{ID: "t1", Title: "Ship it", Status: "completed"}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if active, ok := store.Active(); ok {
		t.Errorf("Active = %+v, want nothing when the plan is finished", active)
	}
}
