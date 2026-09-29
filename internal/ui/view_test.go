package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

func newTestModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	application := testApp(t)
	return New(application)
}

func resize(model *Model, width, height int) {
	_, _ = model.Update(tea.WindowSizeMsg{Width: width, Height: height})
}

// TestModelRendersAtEveryCommonSize catches layout and render panics, which is
// what the earlier lipgloss width mistakes produced.
func TestModelRendersAtEveryCommonSize(t *testing.T) {
	sizes := []struct{ width, height int }{
		{80, 24},
		{120, 40},
		{100, 30},
		{60, 18},
		{50, 16},
		{40, 12},
	}
	for _, size := range sizes {
		model := newTestModel(t)
		resize(model, size.width, size.height)
		view := model.View()
		if strings.TrimSpace(view) == "" {
			t.Errorf("%dx%d produced an empty view", size.width, size.height)
		}
	}
}

func TestTooSmallTerminalShowsAHint(t *testing.T) {
	model := newTestModel(t)
	resize(model, 20, 5)
	if !strings.Contains(model.View(), "too small") {
		t.Errorf("a cramped terminal should explain itself, got %q", model.View())
	}
}

func TestChatViewRendersATranscript(t *testing.T) {
	model := newTestModel(t)
	resize(model, 100, 30)
	model.current = modeChat
	model.blocks = append(model.blocks,
		block{kind: blockUser, text: "refactor the parser"},
		block{kind: blockThinking, reasoning: "Looking at the parser first.", seconds: 3},
		block{kind: blockTool, toolLabel: "Read parser.go", toolOK: true, toolMillis: 8},
		block{kind: blockAssistant, text: "## Summary\n\n- split the tokenizer\n- added tests\n"},
		block{kind: blockPlan, plan: []agent.Todo{{Title: "Split tokenizer", Status: "completed"}, {Title: "Add tests", Status: "in_progress"}}},
	)
	model.refresh()

	view := stripANSI(model.View())
	for _, want := range []string{"refactor the parser", "Reasoned for 3s", "Read parser.go", "Summary", "Split tokenizer"} {
		if !strings.Contains(view, want) {
			t.Errorf("the chat view is missing %q:\n%s", want, view)
		}
	}
}

func TestHelpAndSetupViewsRender(t *testing.T) {
	model := newTestModel(t)
	resize(model, 100, 30)

	model.current = modeHelp
	if view := stripANSI(model.View()); !strings.Contains(view, "/model") || !strings.Contains(view, "Keys") {
		t.Errorf("the help view is incomplete:\n%s", view)
	}

	model.current = modeSetup
	model.setup.step = setupTelegramPair
	model.setup.pairingCode = "123456"
	if view := stripANSI(model.View()); !strings.Contains(view, "123456") {
		t.Errorf("the pairing step should show the code:\n%s", view)
	}
}

func TestApprovalViewRendersChoices(t *testing.T) {
	model := newTestModel(t)
	resize(model, 100, 30)
	model.current = modeChat
	model.pendingApproval = &agent.ApprovalRequest{Tool: "run_command", Risk: "command", Detail: "Running pnpm test"}
	view := stripANSI(model.View())
	for _, want := range []string{"Approval needed", "run_command", "allow once", "deny"} {
		if !strings.Contains(view, want) {
			t.Errorf("the approval prompt is missing %q:\n%s", want, view)
		}
	}
}

func TestSlashMenuAppearsWhileTyping(t *testing.T) {
	model := newTestModel(t)
	resize(model, 100, 30)
	model.current = modeChat
	model.composer.SetValue("/mo")
	model.updateSlashMatches()
	if len(model.slashMatches) != 1 || model.slashMatches[0].Trigger != "/model" {
		t.Fatalf("expected the /model entry, got %+v", model.slashMatches)
	}
	if view := stripANSI(model.View()); !strings.Contains(view, "Show or switch the model") {
		t.Errorf("the slash menu should render:\n%s", view)
	}
}

func TestPlainModeStreamsOnce(t *testing.T) {
	// RunOnce must print the answer and not require a terminal.
	t.Setenv(config.EnvHome, t.TempDir())
	application := testApp(t)
	var builder strings.Builder
	if err := RunOnce(application, "hello", &builder); err == nil {
		// No model configured means a clean error rather than a panic.
		t.Fatalf("expected the missing-model error")
	}
	if strings.Contains(builder.String(), "\x1b[") {
		t.Errorf("piped output must not contain colour escapes: %q", builder.String())
	}
}
