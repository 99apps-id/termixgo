package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/provider"
)

// A resumed session shows each tool call with its label, its real outcome and
// its output preview, matched to the stored result by call id.
func TestRestoreSessionRebuildsToolOutcomes(t *testing.T) {
	model := chatModel(t)
	session := agent.NewSession(t.TempDir(), "test-model")
	session.AddUser("run the tests")
	session.AddAssistant("", "", []provider.ToolCall{
		{ID: "c1", Name: "run_command", Arguments: `{"command":"go test ./..."}`},
		{ID: "c2", Name: "read_file", Arguments: `{"path":"missing.go"}`},
	}, nil)
	session.AddToolResult("c1", "run_command", "ok  pkg/a\nFAIL pkg/b")
	session.AddToolResult("c2", "read_file", "read_file failed: open missing.go: no such file")

	model.restoreSession(session)

	var tools []block
	for _, item := range model.blocks {
		if item.kind == blockTool {
			tools = append(tools, item)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("tool blocks = %d", len(tools))
	}
	if !tools[0].toolOK || tools[0].toolOutput != "ok  pkg/a\nFAIL pkg/b" || tools[0].toolLabel == "" {
		t.Fatalf("the command call should be restored with its output: %+v", tools[0])
	}
	if tools[1].toolOK || !strings.Contains(tools[1].toolOutput, "no such file") {
		t.Fatalf("the failed read should be restored as a failure: %+v", tools[1])
	}
	if view := display(model); !strings.Contains(view, "⎿  ok  pkg/a") {
		t.Fatalf("the restored preview should be painted:\n%s", view)
	}
}

// The newest answer replaces only the text after a steer, so the answer the
// model gave before the steer stays as streamed.
func TestReconcileKeepsTheAnswerBeforeASteer(t *testing.T) {
	model := chatModel(t)
	model.blocks = append(model.blocks,
		block{kind: blockUser, text: "task"},
		block{kind: blockAssistant, text: "first part"},
		block{kind: blockUser, text: "steer: also do X"},
		block{kind: blockAssistant, text: "partial"},
	)
	session := model.app.Session()
	model.turnMark = session.MessageCount()
	session.AddAssistant("final answer after the steer", "", nil, nil)

	model.reconcileAnswerText()
	if model.blocks[len(model.blocks)-3].text != "first part" || model.blocks[len(model.blocks)-1].text != "final answer after the steer" {
		t.Fatalf("blocks = %+v", model.blocks[len(model.blocks)-4:])
	}
}
