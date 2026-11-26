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

	// The live turn now folds in a steer that arrived while its answer was
	// streaming, instead of leaving it behind for a separate turn.
	if got := model.app.SteerCount(); got != 0 {
		t.Fatalf("live turn should have consumed the steer, got %d remaining", got)
	}
	found := false
	for _, msg := range model.app.Session().Messages() {
		if strings.Contains(msg.Content, "also check the tests") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("live turn did not record the steer in the session")
	}

	// A steer that lands after the loop closed cannot be folded in, so the UI
	// drains it on runDoneMsg and runs it as the next turn without duplicating
	// its transcript block.
	model.app.Steer("run this after the turn")
	before := len(model.app.Session().Messages())
	blocksBefore := len(queued.blocks)
	next, _ := send(t, queued, runDoneMsg{})
	if len(next.queue) != 0 || len(next.lateSteers) != 0 {
		t.Errorf("the late steer should have started, got queue=%d lateSteers=%d", len(next.queue), len(next.lateSteers))
	}
	if !next.running {
		t.Errorf("a late steer should start the next turn")
	}
	for _, b := range next.blocks[blocksBefore:] {
		if b.kind == blockUser {
			t.Errorf("late steer duplicated a user block: %+v", b)
		}
	}
	// That turn runs on its own goroutine and writes the workspace state
	// directory. Ending the test under it races the temp-dir cleanup, which
	// on Windows fails on the files the turn still has open.
	waitForTurnToSettle(t, model, before)
}

// TestSteerNoticeClearsWhenNothingWaits covers the phantom count: a steer the
// live turn consumed leaves no drain behind, so the notice must not keep
// claiming a wait on the idle chat.
func TestSteerNoticeClearsWhenNothingWaits(t *testing.T) {
	model := chatModel(t)
	model.notice = "Steering the run. (1 waiting)"
	next, _ := send(t, model, runDoneMsg{})
	if next.notice != "" {
		t.Errorf("stale steer notice = %q, want it cleared with nothing waiting", next.notice)
	}
}

// TestSteerNoticeRefreshesWhileTheTurnRuns covers the mid-turn staleness: the
// count is recomputed from what is actually still waiting on every event.
func TestSteerNoticeRefreshesWhileTheTurnRuns(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.app.Steer("first")
	model.app.Steer("second")
	model.notice = "Steering the run. (2 waiting)"
	model.applyEvent(agent.Event{Kind: agent.EventContext})
	if model.notice != "Steering the run. (2 waiting)" {
		t.Fatalf("notice = %q, want the live count kept", model.notice)
	}
	if taken := model.app.TakeSteer(); len(taken) != 2 {
		t.Fatalf("TakeSteer = %q, want both steers", taken)
	}
	model.applyEvent(agent.Event{Kind: agent.EventContext})
	if model.notice != "" {
		t.Errorf("notice = %q, want it cleared once nothing waits", model.notice)
	}
}

// TestOtherNoticesSurviveTheSteerRefresh guards the scope: only the steering
// notice is recomputed, never an unrelated one.
func TestOtherNoticesSurviveTheSteerRefresh(t *testing.T) {
	model := chatModel(t)
	model.notice = "Queue is full (5). Wait for the current turn."
	model.applyEvent(agent.Event{Kind: agent.EventContext})
	if model.notice != "Queue is full (5). Wait for the current turn." {
		t.Errorf("an unrelated notice was rewritten: %q", model.notice)
	}
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

// A plan whose items are all done is history: it leaves the status line, while
// an unfinished one keeps its counter.
func TestFinishedPlanLeavesTheStatusLine(t *testing.T) {
	model := chatModel(t)
	model.app.Todos().Set([]agent.Todo{
		{ID: "t1", Title: "One", Status: "completed"},
		{ID: "t2", Title: "Two", Status: "completed"},
	})
	if view := display(model); strings.Contains(view, "plan 2/2") {
		t.Errorf("a finished plan should not stay in the status line:\n%s", view)
	}
}
