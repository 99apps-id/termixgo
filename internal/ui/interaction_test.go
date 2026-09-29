package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/app"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

// chatModel builds a model that is already set up, so the UI starts in chat
// mode rather than opening the onboarding wizard.
//
// A fresh install has no default model, which makes New open the wizard and
// send every keystroke to the picker. Tests that exercise the chat loop need a
// configured model to reach it. The model is a local one, so no API key is
// needed and nothing is called over the network during construction.
func chatModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())

	cfg := config.Default()
	cfg.DefaultModel = "qwen2.5-coder:latest"
	cfg.ApprovalMode = config.ApprovalAll
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	application, err := app.New(t.TempDir())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if !application.HasModel() {
		t.Fatalf("the fixture should have a usable model")
	}

	model := New(application)
	if model.current != modeChat {
		t.Fatalf("the fixture should start in chat mode, got mode %d", model.current)
	}
	resize(model, 120, 40)
	return model
}

// key builds a key message from its name, the way Bubble Tea delivers one.
func key(name string) tea.KeyMsg {
	switch name {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "ctrl+o":
		return tea.KeyMsg{Type: tea.KeyCtrlO}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
	}
}

// press sends one key through Update and returns the model.
func press(t *testing.T, model *Model, name string) *Model {
	t.Helper()
	next, _ := model.Update(key(name))
	updated, ok := next.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", next)
	}
	return updated
}

// display refreshes the viewport and returns the rendered view without colour.
//
// applyEvent updates the transcript; refresh is what the real Update loop calls
// afterwards to push it into the viewport, so a test that reads the view has to
// do the same.
func display(model *Model) string {
	model.refresh()
	return stripANSI(model.View())
}

// ---------------------------------------------------------------- event folding

// TestThinkingAccumulatesThenCloses is the reasoning display contract: chunks
// join one live block, and the closing event turns it into a timed one.
func TestThinkingAccumulatesThenCloses(t *testing.T) {
	model := chatModel(t)

	model.applyEvent(agent.Event{Kind: agent.EventThinking, Text: "first "})
	model.applyEvent(agent.Event{Kind: agent.EventThinking, Text: "second"})

	thinking := 0
	for _, item := range model.blocks {
		if item.kind != blockThinking {
			continue
		}
		thinking++
		if item.reasoning != "first second" {
			t.Errorf("reasoning = %q, want the chunks joined", item.reasoning)
		}
		if !item.running {
			t.Errorf("the block should still be running")
		}
	}
	if thinking != 1 {
		t.Fatalf("chunks must share one block, got %d", thinking)
	}

	model.applyEvent(agent.Event{Kind: agent.EventReasoned, Text: "first second", ToolMillis: 4200})
	for _, item := range model.blocks {
		if item.kind != blockThinking {
			continue
		}
		if item.running {
			t.Errorf("the block should no longer be running")
		}
		if item.seconds != 4 {
			t.Errorf("seconds = %d, want 4", item.seconds)
		}
	}
	if view := display(model); strings.Contains(view, "Thinking...") {
		t.Errorf("a finished block must not still read as thinking:\n%s", view)
	}
}

func TestTextChunksJoinOneBlock(t *testing.T) {
	model := chatModel(t)

	model.applyEvent(agent.Event{Kind: agent.EventText, Text: "Hello "})
	model.applyEvent(agent.Event{Kind: agent.EventText, Text: "world"})

	assistants := 0
	for _, item := range model.blocks {
		if item.kind != blockAssistant {
			continue
		}
		assistants++
		if item.text != "Hello world" {
			t.Errorf("text = %q", item.text)
		}
	}
	if assistants != 1 {
		t.Fatalf("text chunks must share one block, got %d", assistants)
	}
}

// TestTextAfterThinkingClosesTheThinkingBlock is a display guard: a live
// thinking block left next to the answer reads as if the model is still busy.
func TestTextAfterThinkingClosesTheThinkingBlock(t *testing.T) {
	model := chatModel(t)

	model.applyEvent(agent.Event{Kind: agent.EventThinking, Text: "hmm"})
	model.applyEvent(agent.Event{Kind: agent.EventText, Text: "Answer"})

	for _, item := range model.blocks {
		if item.kind == blockThinking && item.running {
			t.Fatalf("the thinking block should have closed when the answer started")
		}
	}
}

func TestToolStartAndEndPairUp(t *testing.T) {
	model := chatModel(t)

	model.applyEvent(agent.Event{Kind: agent.EventToolStart, ToolName: "read_file", ToolLabel: "Reading main.go"})
	tools := 0
	for _, item := range model.blocks {
		if item.kind != blockTool {
			continue
		}
		tools++
		if !item.running {
			t.Errorf("the tool should start as running")
		}
	}
	if tools != 1 {
		t.Fatalf("expected one tool block, got %d", tools)
	}

	model.applyEvent(agent.Event{
		Kind: agent.EventToolEnd, ToolName: "read_file", ToolLabel: "Read main.go",
		ToolOK: true, ToolMillis: 12, ToolResult: "content",
	})

	tools = 0
	for _, item := range model.blocks {
		if item.kind != blockTool {
			continue
		}
		tools++
		if item.running {
			t.Errorf("the tool should no longer be running")
		}
		if item.toolLabel != "Read main.go" {
			t.Errorf("label = %q, want the past tense", item.toolLabel)
		}
		if item.toolMillis != 12 {
			t.Errorf("millis = %d", item.toolMillis)
		}
	}
	if tools != 1 {
		t.Fatalf("the end event must update the existing block, got %d blocks", tools)
	}
}

func TestToolEndWithoutAStartStillRenders(t *testing.T) {
	model := chatModel(t)

	// A dropped start event must not lose the result entirely.
	model.applyEvent(agent.Event{Kind: agent.EventToolEnd, ToolName: "grep", ToolLabel: "Searched x", ToolOK: true})

	found := false
	for _, item := range model.blocks {
		if item.kind == blockTool && item.toolName == "grep" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a tool end without a start should still be recorded")
	}
}

func TestFailedToolIsMarkedFailed(t *testing.T) {
	model := chatModel(t)

	model.applyEvent(agent.Event{Kind: agent.EventToolStart, ToolName: "run_command", ToolLabel: "Running tests"})
	model.applyEvent(agent.Event{Kind: agent.EventToolEnd, ToolName: "run_command", ToolLabel: "Ran tests", ToolOK: false})

	for _, item := range model.blocks {
		if item.kind == blockTool && item.toolOK {
			t.Fatalf("a failed tool must not be marked ok")
		}
	}
	if view := display(model); !strings.Contains(view, "Ran tests") {
		t.Errorf("the failure should still be visible:\n%s", view)
	}
}

func TestPlanReplacesTheLatestPlanBlock(t *testing.T) {
	model := chatModel(t)

	model.applyEvent(agent.Event{Kind: agent.EventPlan, Plan: []agent.Todo{{Title: "one", Status: "in_progress"}}})
	model.applyEvent(agent.Event{Kind: agent.EventPlan, Plan: []agent.Todo{
		{Title: "one", Status: "completed"},
		{Title: "two", Status: "in_progress"},
	}})

	plans := 0
	for _, item := range model.blocks {
		if item.kind != blockPlan {
			continue
		}
		plans++
		if len(item.plan) != 2 {
			t.Errorf("the plan should be replaced, got %d items", len(item.plan))
		}
	}
	if plans != 1 {
		t.Fatalf("expected one plan block, got %d", plans)
	}
}

func TestErrorsAndNoticesAppearInTheTranscript(t *testing.T) {
	model := chatModel(t)

	model.applyEvent(agent.Event{Kind: agent.EventNotice, Text: "note one"})
	model.applyEvent(agent.Event{Kind: agent.EventError, Err: errFixture{}})

	view := display(model)
	if !strings.Contains(view, "note one") {
		t.Errorf("the notice is missing:\n%s", view)
	}
	if !strings.Contains(view, "fixture failure") {
		t.Errorf("the error is missing:\n%s", view)
	}
}

// TestStepCapExplainsItself covers the case where a run stops at the budget and
// the operator has no idea why nothing more happened.
func TestStepCapExplainsItself(t *testing.T) {
	model := chatModel(t)

	model.applyEvent(agent.Event{Kind: agent.EventTurnEnd, StopReason: "step-cap"})
	if view := display(model); !strings.Contains(view, "step budget") {
		t.Errorf("a step-capped run must say so:\n%s", view)
	}
}

// TestStatusBarShowsTokenUsage checks the number the operator watches.
//
// The status bar reads the app, not the event stream: the app is what
// accumulates usage and cost, and the UI only renders it. Asserting on an
// event here would test a path that does not exist.
func TestStatusBarShowsTokenUsage(t *testing.T) {
	model := chatModel(t)
	model.app.AddUsage(provider.Usage{PromptTokens: 1200, CompletionTokens: 300, TotalTokens: 1500})

	if view := display(model); !strings.Contains(view, "tokens 1500") {
		t.Errorf("the token count should reach the status bar:\n%s", view)
	}
}

// TestStatusBarHidesZeroSpend keeps a free session from looking like a billed
// one, and keeps a row of noise out of the bar.
func TestStatusBarHidesZeroSpend(t *testing.T) {
	model := chatModel(t)

	// The fixture is a local model, so the cost is known and zero.
	if _, known := model.app.Cost(); !known {
		t.Fatalf("the local fixture should report a known cost")
	}
	if view := display(model); strings.Contains(view, "$") {
		t.Errorf("nothing has been spent, so no amount should be shown:\n%s", view)
	}
}

type errFixture struct{}

func (errFixture) Error() string { return "fixture failure" }

// ---------------------------------------------------------------- key routing

func TestCtrlCQuits(t *testing.T) {
	model := chatModel(t)
	_, cmd := model.Update(key("ctrl+c"))
	if cmd == nil {
		t.Fatalf("ctrl+c should return a command")
	}
	if msg := cmd(); msg == nil {
		t.Fatalf("ctrl+c should produce a message")
	}
}

func TestEnterWithAnEmptyComposerDoesNothing(t *testing.T) {
	model := chatModel(t)
	before := len(model.blocks)

	updated := press(t, model, "enter")
	if len(updated.blocks) != before {
		t.Errorf("an empty submit must not add a block")
	}
	if updated.running {
		t.Errorf("an empty submit must not start a run")
	}
}

func TestEnterSubmitsAndClearsTheComposer(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("/status")

	updated := press(t, model, "enter")

	if updated.composer.Value() != "" {
		t.Errorf("the composer should be cleared, got %q", updated.composer.Value())
	}
	if view := display(updated); !strings.Contains(view, "workspace:") {
		t.Errorf("/status should have produced a status block:\n%s", view)
	}
}

func TestEscStopsARunningTurn(t *testing.T) {
	model := chatModel(t)
	model.running = true

	updated := press(t, model, "esc")
	if updated.notice == "" {
		t.Errorf("stopping should tell the operator what is happening")
	}
	if view := display(updated); !strings.Contains(strings.ToLower(view), "stopping") {
		t.Errorf("the notice should be visible:\n%s", view)
	}
}

func TestEscClearsTheComposerWhenIdle(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("half a thought")

	updated := press(t, model, "esc")
	if updated.composer.Value() != "" {
		t.Errorf("esc should clear the composer, got %q", updated.composer.Value())
	}
}

func TestEnterWhileRunningQueues(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.composer.SetValue("a second request")
	before := len(model.blocks)

	updated := press(t, model, "enter")
	if len(updated.blocks) != before {
		t.Errorf("a queued submit must not add a block yet")
	}
	if updated.composer.Value() != "" {
		t.Errorf("the composer should be cleared for the next steer, got %q", updated.composer.Value())
	}
	if len(updated.queue) != 1 || updated.queue[0] != "a second request" {
		t.Errorf("queue = %q, want the second request held", updated.queue)
	}
	if updated.notice == "" {
		t.Errorf("the queueing should be explained")
	}
}

func TestTabCompletesASlashCommand(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("/mo")
	model.updateSlashMatches()

	updated := press(t, model, "tab")
	if !strings.HasPrefix(updated.composer.Value(), "/model") {
		t.Errorf("tab should complete to /model, got %q", updated.composer.Value())
	}
	if len(updated.slashMatches) != 0 {
		t.Errorf("the menu should close after completing")
	}
}

func TestArrowKeysMoveTheSlashCursor(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("/")
	model.updateSlashMatches()
	if len(model.slashMatches) < 3 {
		t.Fatalf("the fixture needs at least three commands, got %d", len(model.slashMatches))
	}

	model = press(t, model, "down")
	if model.slashCursor != 1 {
		t.Errorf("down should advance the cursor, got %d", model.slashCursor)
	}
	model = press(t, model, "up")
	if model.slashCursor != 0 {
		t.Errorf("up should retreat the cursor, got %d", model.slashCursor)
	}
	// Wrapping backwards from the top lands on the last entry.
	model = press(t, model, "up")
	if model.slashCursor != len(model.slashMatches)-1 {
		t.Errorf("up from the top should wrap, got %d", model.slashCursor)
	}
}

func TestHelpModeClosesOnAnyKey(t *testing.T) {
	model := chatModel(t)
	model.current = modeHelp

	updated := press(t, model, "x")
	if updated.current != modeChat {
		t.Errorf("any key should close help, got mode %d", updated.current)
	}
}

// ---------------------------------------------------------------- approval

// TestApprovalKeysMapToDecisions is a safety contract. A key that maps to the
// wrong decision either blocks work that was approved or runs work that was
// refused, so each mapping is asserted by name.
func TestApprovalKeysMapToDecisions(t *testing.T) {
	cases := []struct {
		key  string
		want agent.Decision
	}{
		{"y", agent.DecisionAllowOnce},
		{"enter", agent.DecisionAllowOnce},
		{"s", agent.DecisionAllowSession},
		{"a", agent.DecisionAllowAlways},
		{"n", agent.DecisionDeny},
		{"esc", agent.DecisionDeny},
	}
	for _, testCase := range cases {
		model := chatModel(t)
		reply := make(chan agent.Decision, 1)
		model.pendingApproval = &agent.ApprovalRequest{Tool: "run_command", Risk: "command", Detail: "Running tests"}
		model.approvalReply = reply

		press(t, model, testCase.key)

		select {
		case got := <-reply:
			if got != testCase.want {
				t.Errorf("key %q produced %v, want %v", testCase.key, got, testCase.want)
			}
		default:
			t.Errorf("key %q did not answer the request", testCase.key)
		}
		if model.pendingApproval != nil {
			t.Errorf("key %q should clear the pending request", testCase.key)
		}
	}
}

// TestApprovalArrowsSelectOptions proves the dialog is navigable without
// letter keys: down twice plus Enter lands on "always".
func TestApprovalArrowsSelectOptions(t *testing.T) {
	model := chatModel(t)
	reply := make(chan agent.Decision, 1)
	model.pendingApproval = &agent.ApprovalRequest{Tool: "run_command", Risk: "command", Detail: "Running tests"}
	model.approvalReply = reply

	moved := press(t, model, "down")
	if moved.approvalCursor != 1 {
		t.Fatalf("down should highlight the second option, got %d", moved.approvalCursor)
	}
	if moved.pendingApproval == nil {
		t.Fatalf("moving must keep the request pending")
	}
	moved = press(t, moved, "down")
	answered, _ := moved.Update(key("enter"))
	final := answered.(*Model)

	select {
	case got := <-reply:
		if got != agent.DecisionAllowAlways {
			t.Errorf("down down enter produced %v, want always", got)
		}
	default:
		t.Errorf("enter did not answer the request")
	}
	if final.pendingApproval != nil {
		t.Errorf("enter should clear the pending request")
	}
	if view := display(final); !strings.Contains(view, "always allowed") {
		t.Errorf("the transcript should record the decision:\n%s", view)
	}
}

// TestApprovalArrowsWrapAround keeps navigation endless: up from the top
// lands on deny, and Enter there denies.
func TestApprovalArrowsWrapAround(t *testing.T) {
	model := chatModel(t)
	reply := make(chan agent.Decision, 1)
	model.pendingApproval = &agent.ApprovalRequest{Tool: "run_command"}
	model.approvalReply = reply

	moved := press(t, model, "up")
	if moved.approvalCursor != len(approvalOptions)-1 {
		t.Fatalf("up from the top should wrap, got %d", moved.approvalCursor)
	}
	answered, _ := moved.Update(key("enter"))
	select {
	case got := <-reply:
		_ = answered
		if got != agent.DecisionDeny {
			t.Errorf("wrapped enter produced %v, want deny", got)
		}
	default:
		t.Errorf("enter did not answer the request")
	}
}

// TestApprovalViewMarksTheHighlight pins the visual: the cursor row carries
// the marker while the key hints stay visible.
func TestApprovalViewMarksTheHighlight(t *testing.T) {
	model := chatModel(t)
	resize(model, 100, 30)
	model.pendingApproval = &agent.ApprovalRequest{Tool: "run_command", Risk: "command", Detail: "Running tests"}
	model.approvalCursor = 1
	view := stripANSI(model.View())
	for _, want := range []string{"Approval needed", "allow session", "Enter select"} {
		if !strings.Contains(view, want) {
			t.Errorf("the approval view is missing %q:\n%s", want, view)
		}
	}
}

// TestUnknownApprovalKeyKeepsAsking is the safe failure: a stray keypress must
// not be read as consent.
func TestUnknownApprovalKeyKeepsAsking(t *testing.T) {
	model := chatModel(t)
	reply := make(chan agent.Decision, 1)
	model.pendingApproval = &agent.ApprovalRequest{Tool: "run_command"}
	model.approvalReply = reply

	updated := press(t, model, "q")

	if updated.pendingApproval == nil {
		t.Fatalf("an unrecognised key must leave the request pending")
	}
	select {
	case decision := <-reply:
		t.Fatalf("an unrecognised key must not answer, got %v", decision)
	default:
	}
	if !strings.Contains(stripANSI(updated.View()), "Approval needed") {
		t.Errorf("the prompt should still be on screen")
	}
}

// TestAllowAlwaysIsRemembered checks the one decision that outlives the run.
func TestAllowAlwaysIsRemembered(t *testing.T) {
	model := chatModel(t)
	reply := make(chan agent.Decision, 1)
	model.pendingApproval = &agent.ApprovalRequest{Tool: "write_file"}
	model.approvalReply = reply

	updated := press(t, model, "a")

	if !updated.app.Policy().SessionAllowed["write_file"] {
		t.Errorf("allow-always should be recorded in the policy")
	}
	if view := display(updated); !strings.Contains(view, "always allowed") {
		t.Errorf("the transcript should record the decision:\n%s", view)
	}
}

func TestApprovalDecisionIsRecordedInTheTranscript(t *testing.T) {
	model := chatModel(t)
	reply := make(chan agent.Decision, 1)
	model.pendingApproval = &agent.ApprovalRequest{Tool: "run_command"}
	model.approvalReply = reply

	updated := press(t, model, "n")

	view := display(updated)
	if !strings.Contains(view, "denied") || !strings.Contains(view, "run_command") {
		t.Errorf("the transcript should record who was denied:\n%s", view)
	}
}

// ---------------------------------------------------------------- questions

func TestAskKeyAnswersAndEscDeclines(t *testing.T) {
	model := chatModel(t)
	reply := make(chan string, 1)
	model.pendingAsk = &askRequestMsg{question: "which port?", options: []string{"3000", "8080"}, reply: reply}

	model.input.SetValue("3000")
	press(t, model, "enter")

	if got := <-reply; got != "3000" {
		t.Errorf("answer = %q", got)
	}
	if model.pendingAsk != nil {
		t.Errorf("the question should be cleared")
	}

	second := chatModel(t)
	secondReply := make(chan string, 1)
	second.pendingAsk = &askRequestMsg{question: "again?", reply: secondReply}
	press(t, second, "esc")
	if got := <-secondReply; got != "(no answer)" {
		t.Errorf("declining should send a clear no-answer marker, got %q", got)
	}
}

func TestAskWithAnEmptyAnswerKeepsWaiting(t *testing.T) {
	model := chatModel(t)
	reply := make(chan string, 1)
	model.pendingAsk = &askRequestMsg{question: "which port?", reply: reply}

	updated := press(t, model, "enter")

	if updated.pendingAsk == nil {
		t.Fatalf("an empty answer must not resolve the question")
	}
	select {
	case got := <-reply:
		t.Fatalf("an empty answer must not be sent, got %q", got)
	default:
	}
}

// ---------------------------------------------------------------- slash dispatch

// TestEverySlashCommandIsHandled walks the whole catalogue, so a command that
// panics or falls through to "unknown" is caught when it is added, not by a
// user.
func TestEverySlashCommandIsHandled(t *testing.T) {
	for _, command := range SlashCommands() {
		name := strings.TrimPrefix(command.Trigger, "/")
		if name == "exit" {
			// Quitting is covered separately: it would end the walk.
			continue
		}
		t.Run(name, func(t *testing.T) {
			model := chatModel(t)
			next, _ := model.runSlash(name, "")
			final, ok := next.(*Model)
			if !ok {
				t.Fatalf("runSlash returned %T", next)
			}
			if view := display(final); strings.Contains(view, "Unknown command") {
				t.Errorf("/%s is in the catalogue but is not handled", name)
			}
		})
	}
}

func TestExitCommandQuits(t *testing.T) {
	model := chatModel(t)
	_, cmd := model.runSlash("exit", "")
	if cmd == nil {
		t.Fatalf("/exit should return a command")
	}
}

func TestUnknownSlashCommandNamesItself(t *testing.T) {
	model := chatModel(t)
	next, _ := model.runSlash("nonsense", "")
	final := next.(*Model)

	view := display(final)
	if !strings.Contains(view, "nonsense") {
		t.Errorf("the error should name the command:\n%s", view)
	}
	if !strings.Contains(view, "/help") {
		t.Errorf("the error should point at help:\n%s", view)
	}
}

func TestTrustCommandRoundTrip(t *testing.T) {
	model := chatModel(t)

	next, _ := model.runSlash("trust", "on")
	model = next.(*Model)
	if !model.app.Trusted() {
		t.Fatalf("/trust on did not trust the folder")
	}
	if view := display(model); !strings.Contains(view, "now trusted") {
		t.Errorf("the change should be reported:\n%s", view)
	}

	next, _ = model.runSlash("trust", "off")
	model = next.(*Model)
	if model.app.Trusted() {
		t.Fatalf("/trust off did not untrust the folder")
	}

	next, _ = model.runSlash("trust", "sideways")
	model = next.(*Model)
	if view := display(model); !strings.Contains(view, "Usage") {
		t.Errorf("an invalid argument should show usage:\n%s", view)
	}
}

func TestApprovalCommandValidates(t *testing.T) {
	model := chatModel(t)

	next, _ := model.runSlash("approval", "ask")
	model = next.(*Model)
	if string(model.appConfig().ApprovalMode) != "ask" {
		t.Errorf("approval mode = %q, want ask", model.appConfig().ApprovalMode)
	}

	next, _ = model.runSlash("approval", "bogus")
	model = next.(*Model)
	if view := display(model); !strings.Contains(view, "ask, edits, all") {
		t.Errorf("an invalid mode should list the valid ones:\n%s", view)
	}
}

func TestToolsCommandListsTheFullSet(t *testing.T) {
	model := chatModel(t)
	next, _ := model.runSlash("tools", "")
	final := next.(*Model)

	view := display(final)
	for _, want := range []string{"read_file", "run_background", "git_commit", "edit"} {
		if !strings.Contains(view, want) {
			t.Errorf("/tools should list %s:\n%s", want, view)
		}
	}
}

func TestSkillsCommandHandlesNoSkills(t *testing.T) {
	model := chatModel(t)
	next, _ := model.runSlash("skills", "reload")
	final := next.(*Model)

	// The fixture has no skills, so the honest answer is to say so and where
	// to put one.
	if view := display(final); !strings.Contains(strings.ToLower(view), "skill") {
		t.Errorf("/skills should say something about skills:\n%s", view)
	}
}

func TestMemoryAndPlanCommandsHandleEmptyState(t *testing.T) {
	model := chatModel(t)

	next, _ := model.runSlash("memory", "")
	model = next.(*Model)
	if view := display(model); !strings.Contains(strings.ToLower(view), "learned") {
		t.Errorf("/memory with nothing stored should say so:\n%s", view)
	}

	next, _ = model.runSlash("plan", "")
	model = next.(*Model)
	if view := display(model); !strings.Contains(strings.ToLower(view), "plan is empty") {
		t.Errorf("/plan with no plan should say so:\n%s", view)
	}
}

// TestNewCommandClearsTheConversation is the /new contract.
func TestNewCommandClearsTheConversation(t *testing.T) {
	model := chatModel(t)
	model.app.Session().AddUser("hello")
	if model.app.Session().Turns() != 1 {
		t.Fatalf("the fixture should have one turn")
	}

	next, _ := model.runSlash("new", "")
	final := next.(*Model)

	if final.app.Session().Turns() != 0 {
		t.Errorf("turns = %d after /new, want 0", final.app.Session().Turns())
	}
	if view := display(final); !strings.Contains(view, "new session") {
		t.Errorf("the change should be reported:\n%s", view)
	}
}

func TestCostCommandReportsWhatIsKnown(t *testing.T) {
	model := chatModel(t)
	next, _ := model.runSlash("cost", "")
	final := next.(*Model)

	view := display(final)
	if !strings.Contains(view, "Tokens:") {
		t.Errorf("/cost should report tokens:\n%s", view)
	}
	// The fixture is a local model, so the app knows the cost is zero. It must
	// not claim the cost is unknown.
	if !strings.Contains(view, "Estimated spend") {
		t.Errorf("/cost should report spend:\n%s", view)
	}
	if strings.Contains(view, "cost unknown") {
		t.Errorf("a local model is known-free, not unknown:\n%s", view)
	}
}

func TestPsCommandReportsNothingRunning(t *testing.T) {
	model := chatModel(t)

	next, _ := model.runSlash("ps", "")
	model = next.(*Model)
	if view := display(model); !strings.Contains(strings.ToLower(view), "no background processes") {
		t.Errorf("/ps with nothing running should say so:\n%s", view)
	}

	next, _ = model.runSlash("ps", "kill")
	model = next.(*Model)
	if view := display(model); !strings.Contains(view, "Usage") {
		t.Errorf("/ps kill without a handle should show usage:\n%s", view)
	}
}

func TestPsKillUnknownHandleIsRefused(t *testing.T) {
	model := chatModel(t)
	next, _ := model.runSlash("ps", "kill proc-99")
	final := next.(*Model)

	if view := display(final); !strings.Contains(view, "proc-99") {
		t.Errorf("the refusal should name the handle:\n%s", view)
	}
}

func TestModelCommandWithNoArgumentOpensThePicker(t *testing.T) {
	model := chatModel(t)
	next, _ := model.runSlash("model", "")
	final := next.(*Model)

	if final.current != modePicker {
		t.Fatalf("a bare /model should open the picker, got mode %d", final.current)
	}
	if len(final.picker.items) == 0 {
		t.Errorf("the picker should be populated")
	}
}

func TestInitWithoutAModelExplainsSetup(t *testing.T) {
	// A fresh app has no model, which is exactly the case /init has to explain.
	t.Setenv(config.EnvHome, t.TempDir())
	application, err := app.New(t.TempDir())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	model := New(application)
	model.current = modeChat
	resize(model, 120, 40)

	next, cmd := model.runSlash("init", "")
	final := next.(*Model)

	if view := display(final); !strings.Contains(view, "Pick a model") {
		t.Errorf("/init without a model should point at setup:\n%s", view)
	}
	if cmd != nil {
		t.Errorf("/init without a model must not start a run")
	}
}

// ---------------------------------------------------------------- Ctrl+O toggle

// TestCtrlOTogglesDetailVisibility covers the toggle that controls whether
// thinking/reasoning and tool blocks show their full content or a compact line.
func TestCtrlOTogglesDetailVisibility(t *testing.T) {
	model := chatModel(t)
	if !model.showDetails {
		t.Fatalf("details should be visible by default")
	}

	// Press Ctrl+O to hide details.
	hidden := press(t, model, "ctrl+o")
	if hidden.showDetails {
		t.Errorf("Ctrl+O should hide details")
	}
	if !strings.Contains(hidden.notice, "hidden") {
		t.Errorf("the notice should say details are hidden, got %q", hidden.notice)
	}

	// Press Ctrl+O again to show details.
	shown := press(t, hidden, "ctrl+o")
	if !shown.showDetails {
		t.Errorf("a second Ctrl+O should show details")
	}
	if !strings.Contains(shown.notice, "visible") {
		t.Errorf("the notice should say details are visible, got %q", shown.notice)
	}
}

// TestHiddenDetailsCollapsesThinkingAndTool verifies that the transcript omits
// the reasoning body and the tool timing when details are hidden.
func TestHiddenDetailsCollapsesThinkingAndTool(t *testing.T) {
	model := chatModel(t)

	model.applyEvent(agent.Event{Kind: agent.EventThinking, Text: "deep thought"})
	model.applyEvent(agent.Event{Kind: agent.EventReasoned, Text: "deep thought", ToolMillis: 5000})
	model.applyEvent(agent.Event{Kind: agent.EventToolStart, ToolName: "read_file", ToolLabel: "Reading main.go"})
	model.applyEvent(agent.Event{Kind: agent.EventToolEnd, ToolName: "read_file", ToolLabel: "Read main.go", ToolOK: true, ToolMillis: 42})
	model.applyEvent(agent.Event{Kind: agent.EventText, Text: "Here is the answer."})

	// With details visible, the reasoning body and timing appear.
	model.showDetails = true
	expanded := display(model)
	if !strings.Contains(expanded, "deep thought") {
		t.Errorf("expanded view should contain the reasoning body:\n%s", expanded)
	}
	if !strings.Contains(expanded, "42ms") {
		t.Errorf("expanded view should contain the tool timing:\n%s", expanded)
	}

	// With details hidden, only the headers remain.
	model.showDetails = false
	collapsed := display(model)
	if !strings.Contains(collapsed, "Reasoned") {
		t.Errorf("collapsed view should still show the header:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "deep thought") {
		t.Errorf("collapsed view should not contain the reasoning body:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "42ms") {
		t.Errorf("collapsed view should not contain the tool timing:\n%s", collapsed)
	}
	// The assistant answer must always be visible regardless of the toggle.
	if !strings.Contains(collapsed, "Here is the answer") {
		t.Errorf("the assistant text should always be visible:\n%s", collapsed)
	}
}

// TestTranscriptBlocksAreSeparatedByBlankLines verifies that the output has
// visual spacing between blocks so they do not jam together.
func TestTranscriptBlocksAreSeparatedByBlankLines(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	blocks := []block{
		{kind: blockUser, text: "hello"},
		{kind: blockAssistant, text: "world"},
	}
	rendered := transcript(blocks, styles, 80, true)
	// The two blocks must be separated by at least one blank line.
	if !strings.Contains(rendered, "\n\n") {
		t.Errorf("blocks should be separated by a blank line:\n%q", rendered)
	}
}

// TestEnterCompletesWithoutAMenuRefresh covers the stale-cache trap: the
// inline menu is only refreshed on keystrokes, so the very first Enter after
// a programmatic set must still resolve "/stat" to /status via a fresh match.
func TestEnterCompletesWithoutAMenuRefresh(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("/stat")
	model.slashMatches = nil
	model.slashCursor = 0

	submitted := press(t, model, "enter")
	if view := display(submitted); strings.Contains(view, "Unknown command") {
		t.Errorf("Enter should resolve the completion even with a stale menu:\n%s", view)
	} else if !strings.Contains(view, "workspace:") {
		t.Errorf("Enter should have run /status:\n%s", view)
	}
}

// TestSlashQueuesBehindARun pins the one-turn rule: a status command typed
// mid-run waits in the queue instead of starting a second turn.
func TestSlashQueuesBehindARun(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.composer.SetValue("/status")

	queued := press(t, model, "enter")
	if len(queued.queue) != 1 || queued.queue[0] != "/status" {
		t.Fatalf("queue = %q, want the status command held", queued.queue)
	}
}

// TestStopActsOnTheLiveTurn proves the exception: /stop must cancel now,
// not queue behind the turn it is meant to stop.
func TestStopActsOnTheLiveTurn(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.composer.SetValue("/stop")

	next, _ := model.Update(key("enter"))
	updated := next.(*Model)
	if len(updated.queue) != 0 {
		t.Errorf("queue = %q, want /stop to act immediately", updated.queue)
	}
	if view := display(updated); !strings.Contains(view, "Stopping") {
		t.Errorf("/stop should acknowledge the stop:\n%s", view)
	}
}

// TestExitQuitsEvenWhileRunning proves quit bypasses the steer queue too:
// a full queue must not trap the operator in the program.
func TestExitQuitsEvenWhileRunning(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.queue = []string{"held steer"}
	model.composer.SetValue("/exit")

	_, cmd := model.Update(key("enter"))
	if cmd == nil {
		t.Fatalf("exit should return a command")
	}
	if message := cmd(); message == nil {
		t.Fatalf("exit command returned no message")
	}
}

// TestCheckpointSlashRoundTrip proves the undo contract end to end: save a
// checkpoint through the slash handler, break a file, rewind through the
// slash handler, and see the checkpoint content come back.
func TestCheckpointSlashRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	model := chatModel(t)
	workspace := model.app.Workspace()
	runGit := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = workspace
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	runGit("init", "-q")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "test")
	runGit("commit", "--allow-empty", "-qm", "init")
	if err := os.WriteFile(workspace+"/notes.txt", []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	next, _ := model.runSlash("checkpoint", "before breakage")
	saved := next.(*Model)
	if view := display(saved); !strings.Contains(view, "Checkpoint") {
		t.Fatalf("checkpoint should confirm the save:\n%s", view)
	}

	if err := os.WriteFile(workspace+"/notes.txt", []byte("broken\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	next, _ = saved.runSlash("rewind", "")
	restored := next.(*Model)
	if view := display(restored); !strings.Contains(view, "Restored") {
		t.Fatalf("rewind should confirm the restore:\n%s", view)
	}
	data, err := os.ReadFile(workspace + "/notes.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.ReplaceAll(string(data), "\r\n", "\n") != "v2\n" {
		t.Errorf("after rewind the file = %q, want v2", data)
	}
}

// TestCheckpointSlashOutsideGitExplainsItself pins the failure message: the
// command needs a repository and must say so instead of stalling.
func TestCheckpointSlashOutsideGitExplainsItself(t *testing.T) {
	model := chatModel(t)
	next, _ := model.runSlash("checkpoint", "")
	if view := display(next.(*Model)); !strings.Contains(view, "not a git repository") {
		t.Errorf("outside git the command should explain itself:\n%s", view)
	}
}

// TestWorktreeSlashLists proves /worktree reaches the git tool: inside a
// repository the listing names the checkout.
func TestWorktreeSlashLists(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	model := chatModel(t)
	workspace := model.app.Workspace()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@e.com"}, {"config", "user.name", "t"}, {"commit", "--allow-empty", "-qm", "init"}} {
		command := exec.Command("git", args...)
		command.Dir = workspace
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	next, _ := model.runSlash("worktree", "list")
	if view := display(next.(*Model)); !strings.Contains(view, "Worktrees") {
		t.Errorf("/worktree list should name the checkout:\n%s", view)
	}
}

// TestApprovalPlanSwitchesToReadOnly pins the plan mode half of /approval:
// selecting it reports the block, and the app carries the mode forward.
func TestApprovalPlanSwitchesToReadOnly(t *testing.T) {
	model := chatModel(t)
	next, _ := model.runSlash("approval", "plan")
	final := next.(*Model)
	if view := display(final); !strings.Contains(view, "blocked") {
		t.Errorf("/approval plan should explain the block:\n%s", view)
	}
	if got := string(final.app.Config().ApprovalMode); got != "plan" {
		t.Errorf("approval mode = %q, want plan", got)
	}
}
