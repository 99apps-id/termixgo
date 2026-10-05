package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
)

func TestRestoreSessionPopulatesTranscriptAndNotice(t *testing.T) {
	model := chatModel(t)

	session := agent.NewSession(t.TempDir(), "test-model")
	session.AddUser("Hello Termixgo")
	session.AddAssistant("I can help you build terminal tools.", "Thinking carefully about TUI...", nil)
	session.AddUser("What is 2+2?")
	session.AddAssistant("2+2 equals 4.", "", nil)
	session.SetTodos([]agent.Todo{
		{Title: "Step 1: check math", Status: "completed"},
		{Title: "Step 2: celebrate", Status: "pending"},
	})

	model.restoreSession(session)

	if len(model.blocks) == 0 {
		t.Fatalf("restoreSession produced 0 blocks")
	}

	rendered := model.viewport.View()
	if !strings.Contains(rendered, "Hello Termixgo") {
		t.Errorf("rendered transcript missing first user message: %s", rendered)
	}
	if !strings.Contains(rendered, "2+2 equals 4") {
		t.Errorf("rendered transcript missing assistant message: %s", rendered)
	}
	if !strings.Contains(rendered, "Resumed session") {
		t.Errorf("rendered transcript missing Resumed session notice: %s", rendered)
	}
}

func TestNewWithOptionsResumesSession(t *testing.T) {
	app := testApp(t)

	session := agent.NewSession(app.Workspace(), app.Model())
	session.AddUser("Unique prompt from old session")
	session.AddAssistant("Unique answer from old session", "", nil)
	if err := session.Save(); err != nil {
		t.Fatalf("session.Save: %v", err)
	}

	model := NewWithOptions(app, Options{ResumeSessionID: session.ID()})
	resize(model, 120, 40)
	rendered := model.viewport.View()
	if !strings.Contains(rendered, "Unique prompt from old session") {
		t.Errorf("model failed to resume session %s; rendered: %s", session.ID(), rendered)
	}
}
