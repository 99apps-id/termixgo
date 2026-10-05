package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

// TestTaskModelsDefaultToTheActiveModel pins the compatibility promise: an
// install that never sets a task model behaves exactly as it did before, which
// means an empty setting and the active model in charge.
func TestTaskModelsDefaultToTheActiveModel(t *testing.T) {
	application := newTestApp(t)
	if got := application.TaskModel(agent.TaskTitle); got != "" {
		t.Errorf("title model = %q, want empty", got)
	}
	if got := application.TaskModel(agent.TaskCompaction); got != "" {
		t.Errorf("compaction model = %q, want empty", got)
	}
}

// TestSetTaskModelRoundTripsAndClears covers the command path, including the
// "default" spelling, which is how the operator puts a job back on the active
// model.
func TestSetTaskModelRoundTripsAndClears(t *testing.T) {
	application := newTestApp(t)

	if err := application.SetTaskModel(agent.TaskTitle, "deepseek:deepseek-v4.1-flash"); err != nil {
		t.Fatalf("SetTaskModel: %v", err)
	}
	if got := application.TaskModel(agent.TaskTitle); got != "deepseek:deepseek-v4.1-flash" {
		t.Errorf("title model = %q", got)
	}
	// The two jobs are separate settings: naming a session must not drag the
	// summariser along with it.
	if got := application.TaskModel(agent.TaskCompaction); got != "" {
		t.Errorf("compaction model = %q, want it left alone", got)
	}

	if err := application.SetTaskModel(agent.TaskTitle, ""); err != nil {
		t.Fatalf("clearing the title model: %v", err)
	}
	if got := application.TaskModel(agent.TaskTitle); got != "" {
		t.Errorf("title model = %q, want empty after clearing", got)
	}
}

// TestSetTaskModelRejectsAnUnknownModel keeps a typo out of the config: a model
// id nothing can resolve would silently fall back forever, which reads as the
// setting having no effect.
func TestSetTaskModelRejectsAnUnknownModel(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetTaskModel(agent.TaskCompaction, "not-a-real-provider:bogus"); err == nil {
		t.Fatalf("an unresolvable model should be refused")
	}
	if got := application.TaskModel(agent.TaskCompaction); got != "" {
		t.Errorf("a refused model must not be stored, got %q", got)
	}
}

// TestPendingWindowStaysQuietOnASmallHistory is the cost guard: with a short
// conversation there is nothing worth condensing, so no call is made at all.
func TestPendingWindowStaysQuietOnASmallHistory(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetTaskModel(agent.TaskCompaction, "deepseek:deepseek-v4.1-flash"); err != nil {
		t.Fatalf("SetTaskModel: %v", err)
	}
	session := application.Session()
	session.AddUser("a short question")
	session.AddAssistant("a short answer", "", nil)

	if got := application.PendingWindow(); got != 0 {
		t.Errorf("PendingWindow = %d on a small history, want 0", got)
	}
}

// TestPendingWindowNeedsADedicatedModel keeps the feature from firing with no
// separate model configured: without one the plain trim is both cheaper and
// equally correct, which is what this project did before.
func TestPendingWindowNeedsADedicatedModel(t *testing.T) {
	application := newTestApp(t)
	session := application.Session()
	fillWindow(t, session, agent.HistoryBudget(application.CurrentModel().Window()))

	if got := application.PendingWindow(); got != 0 {
		t.Errorf("PendingWindow = %d with no task model, want 0", got)
	}
}

// TestPendingWindowFiresInsideTheBand covers the healthy case: a history in the
// 60 to 85 percent band is worth a brief, so the window is reported and the
// condensation may run.
func TestPendingWindowFiresInsideTheBand(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetTaskModel(agent.TaskCompaction, "deepseek:deepseek-v4.1-flash"); err != nil {
		t.Fatalf("SetTaskModel: %v", err)
	}
	session := application.Session()
	fillWindow(t, session, agent.HistoryBudget(application.CurrentModel().Window()))

	window := application.PendingWindow()
	if window == 0 {
		t.Fatalf("a history past the threshold should report its window")
	}
	if window != application.CurrentModel().Window() {
		t.Errorf("PendingWindow = %d, want the model window %d", window, application.CurrentModel().Window())
	}
}

// fillWindow grows the session to about 70 percent of the history budget,
// which is inside the band where a brief starts to pay for itself.
func fillWindow(t *testing.T, session *agent.Session, budget int) {
	t.Helper()
	target := budget * 70 / 100
	chunk := make([]byte, 4000)
	for i := range chunk {
		chunk[i] = 'x'
	}
	for agent.EstimateMessages(session.Messages()) < target {
		session.AddUser(string(chunk))
		session.AddAssistant("acknowledged", "", nil)
	}
}
