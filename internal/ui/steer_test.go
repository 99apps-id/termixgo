package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// heldChatModel builds a chat model whose provider does not answer until the
// returned release channel is closed. That is what puts a turn genuinely in
// flight, which is the condition the steer handoff depends on.
func heldChatModel(t *testing.T) (*Model, chan struct{}, <-chan struct{}) {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())

	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	cfg := config.Default()
	cfg.DefaultModel = "qwen2.5-coder:latest"
	cfg.ApprovalMode = config.ApprovalAll
	cfg.BaseURLs = map[string]string{"ollama": server.URL}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	application := testApp(t)
	model := New(application)
	resize(model, 120, 40)
	return model, release, started
}

// TestQueuedInputSteersALiveTurn is the handoff rule: while a turn really is
// running the input is given to it, so the agent can change course at its next
// step instead of only after it finishes.
func TestQueuedInputSteersALiveTurn(t *testing.T) {
	model, release, started := heldChatModel(t)

	done := make(chan error, 1)
	go func() { done <- model.app.RunTurn(context.Background(), "look at the parser") }()
	<-started
	// The UI is what marks a run as on screen; startRun sets both.
	model.running = true

	model.composer.SetValue("also check the tests")
	queued := press(t, model, "enter")

	if len(queued.queue) != 0 {
		t.Errorf("a live turn should take the steer, but %d inputs were held back", len(queued.queue))
	}
	if got := model.app.SteerCount(); got != 1 {
		t.Fatalf("the app should hold the steer, got %d", got)
	}
	if queued.composer.Value() != "" {
		t.Errorf("the composer should be cleared, got %q", queued.composer.Value())
	}
	if !strings.Contains(queued.notice, "Steering") {
		t.Errorf("the operator should be told the message is armed, got %q", queued.notice)
	}
	// The steer is shown at once so the screen has a sign it was received; the
	// agent folds it in only at its next step.
	if last := queued.blocks[len(queued.blocks)-1]; last.kind != blockUser || last.text != "also check the tests" {
		t.Errorf("the steer should appear as a user block, got %+v", last)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	// The turn ended before it could take the steer, so the UI has to run it
	// rather than let it disappear.
	steer := model.app.TakeSteer()
	if len(steer) != 1 || steer[0] != "also check the tests" {
		t.Fatalf("late steer = %q, want the queued follow-up", steer)
	}
	model.app.Steer(steer[0])
	before := len(model.app.Session().Messages())
	next, _ := send(t, queued, runDoneMsg{})
	if len(next.queue) != 0 {
		t.Errorf("the late steer should have started, got %d held", len(next.queue))
	}
	if !next.running {
		t.Errorf("a late steer should start the next turn")
	}
	// That turn runs on its own goroutine and writes the workspace state
	// directory. Ending the test under it races the temp-dir cleanup, which
	// on Windows fails on the files the turn still has open.
	waitForTurnToSettle(t, model, before)
}

// waitForTurnToSettle blocks until a turn started after the session held
// before messages has run and released the single-run slot.
func waitForTurnToSettle(t *testing.T, model *Model, before int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(model.app.Session().Messages()) > before && !model.app.Running() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the late-steer turn did not finish in time")
}

// TestActiveTaskIsNamedInTheStatusLine keeps the plan and the work in step: the
// status line names the item the agent is on, not just how many there are.
func TestActiveTaskIsNamedInTheStatusLine(t *testing.T) {
	model := chatModel(t)
	model.app.Todos().Set([]agent.Todo{
		{ID: "t1", Title: "Split the tokenizer", Status: "completed"},
		{ID: "t2", Title: "Wire the parser", Status: "in_progress"},
		{ID: "t3", Title: "Add tests", Status: "pending"},
	})

	view := display(model)
	if !strings.Contains(view, "task Wire the parser") {
		t.Errorf("the status line should name the active task:\n%s", view)
	}
	if !strings.Contains(view, "plan 1/3") {
		t.Errorf("the plan counter should stay:\n%s", view)
	}
}

// TestNoActiveTaskLeavesTheStatusLineClean is the other half: a completed plan
// must not leave a stale task on screen.
func TestNoActiveTaskLeavesTheStatusLineClean(t *testing.T) {
	model := chatModel(t)
	model.app.Todos().Set([]agent.Todo{{ID: "t1", Title: "Ship it", Status: "completed"}})

	if view := display(model); strings.Contains(view, "task ") {
		t.Errorf("a finished plan should not name a task:\n%s", view)
	}
}
