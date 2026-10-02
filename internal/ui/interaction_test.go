package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
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

	application := testApp(t)
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
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
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

// openSlashPalette opens the command palette the way an operator does: a bare
// slash committed with Enter. Typing the slash alone must never open it, so the
// helper also pins that half of the rule.
func openSlashPalette(t *testing.T, model *Model) *Model {
	t.Helper()
	typed := press(t, model, "/")
	if typed.slashOpen {
		t.Fatalf("a slash alone must not open the palette")
	}
	return press(t, typed, "enter")
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

// TestReasonedAfterTextDoesNotAppendASecondThinkingBlock is the swap guard.
//
// The closing EventReasoned arrives after the whole stream, so by then answer
// text has already been placed on top of the thinking block. Matching only the
// tail appended a duplicate reasoning block below the answer, and the operator
// read the reply and the reasoning as reversed.
func TestReasonedAfterTextDoesNotAppendASecondThinkingBlock(t *testing.T) {
	model := chatModel(t)

	model.applyEvent(agent.Event{Kind: agent.EventThinking, Text: "weighing "})
	model.applyEvent(agent.Event{Kind: agent.EventThinking, Text: "options"})
	model.applyEvent(agent.Event{Kind: agent.EventText, Text: "Here is the answer."})
	model.applyEvent(agent.Event{Kind: agent.EventReasoned, Text: "weighing options carefully", ToolMillis: 4200})

	thinking := 0
	assistantIndex, thinkingIndex := -1, -1
	for index, item := range model.blocks {
		switch item.kind {
		case blockThinking:
			thinking++
			thinkingIndex = index
			if item.running {
				t.Errorf("the thinking block should be closed")
			}
			if item.seconds != 4 {
				t.Errorf("seconds = %d, want 4", item.seconds)
			}
			if item.reasoning != "weighing options carefully" {
				t.Errorf("reasoning = %q, want the closed text", item.reasoning)
			}
		case blockAssistant:
			assistantIndex = index
		}
	}
	if thinking != 1 {
		t.Fatalf("want exactly one thinking block, got %d", thinking)
	}
	if thinkingIndex > assistantIndex {
		t.Fatalf("the reasoning rendered below the answer: thinking=%d assistant=%d", thinkingIndex, assistantIndex)
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

// TestStatusBarAlwaysShowsTheVitals pins the contract the operator asked for:
// the token count and the spend are on screen from the start of a session and
// survive a narrow terminal, where the session id and the approval label are
// dropped instead.
func TestStatusBarAlwaysShowsTheVitals(t *testing.T) {
	// The fixture is a local model, so the cost is known and zero.
	model := chatModel(t)
	if _, known := model.app.Cost(); !known {
		t.Fatalf("the local fixture should report a known cost")
	}
	view := display(model)
	for _, want := range []string{"tokens 0", "$0.0000"} {
		if !strings.Contains(view, want) {
			t.Errorf("the bar should show %q before any turn:\n%s", want, view)
		}
	}

	// An unpriced model says so rather than showing a zero that reads as free.
	unpriced := New(unpricedModel(t).app)
	resize(unpriced, 100, 30)
	if _, known := unpriced.app.Cost(); known {
		t.Fatalf("the fixture model must be unpriced")
	}
	if view := display(unpriced); !strings.Contains(view, "cost n/a") {
		t.Errorf("an unpriced model should say cost n/a:\n%s", view)
	}
}

// TestStatusBarKeepsTheVitalsOnANarrowTerminal is the reason the bar is built in
// two groups. The row is a single line clipped to the terminal, so the parts
// that used to sit at the end disappeared exactly when a run was busy.
func TestStatusBarKeepsTheVitalsOnANarrowTerminal(t *testing.T) {
	for _, width := range []int{50, 60, 80, 100} {
		model := chatModel(t)
		resize(model, width, 24)
		model.app.AddUsage(provider.Usage{PromptTokens: 1200, CompletionTokens: 300, TotalTokens: 1500})
		model.app.Todos().Set([]agent.Todo{{ID: "1", Title: strings.Repeat("a long task name ", 3), Status: "in_progress"}})
		model.notice = "Telegram paired."

		row := stripANSI(model.viewStatus())
		if !strings.Contains(row, "tokens 1500") {
			t.Errorf("at %d columns the token count was dropped:\n%s", width, row)
		}
		if !strings.Contains(row, "$") {
			t.Errorf("at %d columns the spend was dropped:\n%s", width, row)
		}
		if len([]rune(row)) > width {
			t.Errorf("at %d columns the row is %d wide, which wraps", width, len([]rune(row)))
		}
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

func TestTabInSlashEntryRunsWithoutLeavingSlash(t *testing.T) {
	model := chatModel(t)
	typed := openSlashPalette(t, model)
	for _, stroke := range []string{"m", "o"} {
		typed = press(t, typed, stroke)
	}

	updated := press(t, typed, "tab")
	if updated.current != modePicker {
		t.Fatalf("Tab on /model should open the model picker, mode is %d", updated.current)
	}
	if updated.picker.action != "model-provider" {
		t.Fatalf("picker action = %q, want the provider picker", updated.picker.action)
	}
	if got := updated.composer.Value(); got != "" {
		t.Errorf("completing from the popup must not leave slash text, got %q", got)
	}
}

func TestArrowKeysMoveTheSlashCursor(t *testing.T) {
	model := chatModel(t)
	model = openSlashPalette(t, model)
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
	// Wrapping backwards from the top lands on the back row past the end.
	model = press(t, model, "up")
	if model.slashCursor != len(model.slashMatches) {
		t.Errorf("up from the top should wrap to back, got %d", model.slashCursor)
	}
}

// TestSlashAloneStaysLiteralUntilEnter is the path fix: pressing / leaves a
// literal slash and no menu, so a request such as "audit di c:/project" stays
// typeable. Enter on the bare slash is what opens the palette.
func TestSlashAloneStaysLiteralUntilEnter(t *testing.T) {
	model := chatModel(t)
	typed := press(t, model, "/")

	if typed.slashOpen || len(typed.slashMatches) != 0 {
		t.Fatalf("a slash alone must not open the menu")
	}
	if got := typed.composer.Value(); got != "/" {
		t.Fatalf("composer = %q after /, want the literal slash", got)
	}
	opened := press(t, typed, "enter")
	if !opened.slashOpen || len(opened.slashMatches) == 0 {
		t.Fatalf("Enter on / should open the slash menu")
	}
	resize(opened, 50, 16)
	if got := frameHeight(opened.screen()); got > 16 {
		t.Fatalf("the slash entry overlay drew %d rows in a 16-row terminal", got)
	}
}

// TestSlashInsideProseStaysLiteral is the Windows-path regression: a slash in
// the middle of a request is ordinary text. Typing c:/project must insert the
// slash rather than stealing the keystroke into the command popup.
func TestSlashInsideProseStaysLiteral(t *testing.T) {
	model := chatModel(t)
	typed := model
	for _, stroke := range []string{"c", ":", "/", "p", "r", "o", "j", "e", "c", "t"} {
		typed = press(t, typed, stroke)
	}

	if typed.slashOpen || len(typed.slashMatches) != 0 {
		t.Fatalf("a slash inside prose must not open the slash menu, matches=%+v", typed.slashMatches)
	}
	if got := typed.composer.Value(); got != "c:/project" {
		t.Fatalf("composer = %q, want %q", got, "c:/project")
	}
}

// TestSlashTypingFiltersThePalette proves the keystrokes after Enter filter the
// palette while the composer stays empty.
func TestSlashTypingFiltersThePalette(t *testing.T) {
	model := chatModel(t)
	typed := openSlashPalette(t, model)
	for _, stroke := range []string{"s", "t", "a", "t"} {
		typed = press(t, typed, stroke)
	}

	if got := typed.composer.Value(); got != "" {
		t.Fatalf("composer = %q while filtering, want no slash text", got)
	}
	if len(typed.slashMatches) == 0 || typed.slashMatches[typed.slashCursor].Trigger != "/status" {
		t.Fatalf("the popup should be on /status, got %+v cursor %d", typed.slashMatches, typed.slashCursor)
	}

	submitted := press(t, typed, "enter")
	if view := display(submitted); !strings.Contains(view, "workspace:") {
		t.Errorf("Enter should have run /status:\n%s", view)
	}
}

// TestSlashEntryKeepsThePopupVisible proves the raw slash line stays in the
// popup while its composer value remains ordinary text.
func TestSlashEntryKeepsThePopupVisible(t *testing.T) {
	model := chatModel(t)
	typed := openSlashPalette(t, model)
	for _, stroke := range []string{"x", "y", "z"} {
		typed = press(t, typed, stroke)
	}

	if !typed.slashOpen {
		t.Fatalf("an unknown slash entry should keep the popup open")
	}
	view := display(typed)
	if strings.Contains(view, "Ask Termixgo to change something") {
		t.Errorf("the composer should be replaced by the popup:\n%s", view)
	}
	if !strings.Contains(view, "> /xyz") {
		t.Errorf("the popup should show the slash entry:\n%s", view)
	}
}

// TestSlashEntryReportsAnUnknownCommand proves an unmatched popup entry still
// resolves through the ordinary command path, then leaves an empty composer.
func TestSlashEntryReportsAnUnknownCommand(t *testing.T) {
	model := chatModel(t)
	typed := openSlashPalette(t, model)
	for _, stroke := range []string{"x", "y", "z"} {
		typed = press(t, typed, stroke)
	}

	submitted := press(t, typed, "enter")
	view := display(submitted)
	if !strings.Contains(view, "Unknown command") || !strings.Contains(view, "/xyz") {
		t.Errorf("an unknown popup command should be reported:\n%s", view)
	}
	if submitted.slashOpen {
		t.Errorf("a submitted command must close the popup")
	}
	if got := submitted.composer.Value(); got != "" {
		t.Errorf("a submitted command must not leave slash text, got %q", got)
	}
}

// TestSlashEntryBackRowAndEscapeDiscardTheToken proves returning to typing
// from the popup starts clean. The intermediate slash token never reaches the
// composer.
func TestSlashEntryBackRowAndEscapeDiscardTheToken(t *testing.T) {
	model := chatModel(t)
	typed := openSlashPalette(t, model)
	for _, stroke := range []string{"s", "t", "a", "t"} {
		typed = press(t, typed, stroke)
	}
	typed.slashCursor = len(typed.slashMatches)

	backed := press(t, typed, "enter")
	if backed.slashOpen || len(backed.slashMatches) != 0 {
		t.Fatalf("the back row should close the popup")
	}
	if got := backed.composer.Value(); got != "" {
		t.Fatalf("the back row should discard the slash token, got %q", got)
	}

	typed = openSlashPalette(t, model)
	typed = press(t, typed, "s")
	escaped := press(t, typed, "esc")
	if escaped.slashOpen || len(escaped.slashMatches) != 0 {
		t.Fatalf("Esc should close the popup")
	}
	if got := escaped.composer.Value(); got != "" {
		t.Fatalf("Esc should discard the slash token, got %q", got)
	}
}

// TestSlashEntryTabOpensArgumentsWithoutLeavingSlash proves a command with a
// closed argument set keeps its unfinished line in popup/picker state.
func TestSlashEntryTabOpensArgumentsWithoutLeavingSlash(t *testing.T) {
	model := chatModel(t)
	typed := openSlashPalette(t, model)
	for _, stroke := range []string{"t", "r", "u", "s", "t"} {
		typed = press(t, typed, stroke)
	}

	opened := press(t, typed, "tab")
	if opened.current != modePicker {
		t.Fatalf("Tab on /trust should open the argument picker, mode is %d", opened.current)
	}
	if opened.picker.action != "slash-arg:trust" {
		t.Fatalf("picker action = %q, want the trust argument menu", opened.picker.action)
	}
	if got := opened.composer.Value(); got != "" {
		t.Fatalf("opening arguments must not leave slash text, got %q", got)
	}
}

// TestCancellingSlashArgumentsReturnsToThePopup proves the slash token goes
// back to the popup rather than reappearing in the composer.
func TestCancellingSlashArgumentsReturnsToThePopup(t *testing.T) {
	model := chatModel(t)
	typed := openSlashPalette(t, model)
	for _, stroke := range []string{"t", "r", "u", "s", "t"} {
		typed = press(t, typed, stroke)
	}
	opened := press(t, typed, "tab")

	cancelled := press(t, opened, "esc")
	if !cancelled.slashOpen {
		t.Fatalf("cancelling arguments should return to the slash popup")
	}
	if got := cancelled.composer.Value(); got != "" {
		t.Fatalf("cancelling arguments must not restore slash text, got %q", got)
	}
	if got := stripANSI(cancelled.viewSlashMenu()); !strings.Contains(got, "> /trust") {
		t.Errorf("the popup should keep the pending slash entry:\n%s", got)
	}
}

// TestSlashAfterProseStaysLiteral pins the rule the other way: a slash typed
// after prose is plain text, so the operator keeps typing into the same line
// instead of being thrown into the menu. Only a line that is exactly a slash
// opens the palette.
func TestSlashAfterProseStaysLiteral(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("draft")

	typed := press(t, model, "/")
	if typed.slashOpen || len(typed.slashMatches) != 0 {
		t.Fatalf("a slash after prose must not open the popup")
	}
	if got := typed.composer.Value(); got != "draft/" {
		t.Fatalf("composer = %q, want the slash appended as text", got)
	}
}

// TestSlashSpaceOpensArguments keeps the argument route discoverable: a command
// followed by a space, committed with Enter from the palette, opens its choices.
func TestSlashSpaceOpensArguments(t *testing.T) {
	model := chatModel(t)
	typed := openSlashPalette(t, model)
	for _, stroke := range []string{"t", "r", "u", "s", "t", " "} {
		typed = press(t, typed, stroke)
	}

	if got := typed.composer.Value(); got != "" {
		t.Fatalf("composer = %q after the slash space, want no slash text", got)
	}
	if !typed.slashOpen {
		t.Fatalf("a slash argument should keep the popup open")
	}

	opened := press(t, typed, "enter")
	if opened.current != modePicker {
		t.Fatalf("Enter on \"/trust \" should open the argument picker, mode is %d", opened.current)
	}
	if opened.picker.action != "slash-arg:trust" {
		t.Fatalf("picker action = %q, want the trust argument menu", opened.picker.action)
	}
	if got := opened.composer.Value(); got != "" {
		t.Fatalf("opening arguments must not leave slash text, got %q", got)
	}
}

// TestSlashEntryDuringARunRoutesCommandsCorrectly proves the hidden slash flow
// works while a turn is running: a live command runs at once and a command
// that starts work is queued, without leaving the token in the composer.
func TestSlashEntryDuringARunRoutesCommandsCorrectly(t *testing.T) {
	for _, command := range []string{"/status", "/new"} {
		model := chatModel(t)
		model.running = true
		typed := openSlashPalette(t, model)
		for _, stroke := range strings.Split(strings.TrimPrefix(command, "/"), "") {
			typed = press(t, typed, stroke)
		}
		ran := press(t, typed, "enter")

		if ran.slashOpen {
			t.Fatalf("%s left the slash popup open", command)
		}
		if got := ran.composer.Value(); got != "" {
			t.Fatalf("%s left slash text, got %q", command, got)
		}
		if command == "/status" {
			if len(ran.queue) != 0 {
				t.Errorf("/status should run at once, got queue %q", ran.queue)
			}
			if len(ran.blocks) == 0 {
				t.Errorf("/status printed nothing")
			}
			continue
		}
		if len(ran.queue) != 1 || ran.queue[0] != "/new" {
			t.Fatalf("queue = %q, want /new waiting", ran.queue)
		}
	}
}

// TestEscDuringSlashEntryStopsTheRunningTurn proves the slash popup never
// captures the stop shortcut. Esc still stops first; a separate Esc closes the
// popup afterward.
func TestEscDuringSlashEntryStopsTheRunningTurn(t *testing.T) {
	model := chatModel(t)
	model.running = true
	typed := openSlashPalette(t, model)
	if !typed.slashOpen {
		t.Fatalf("slash should open the popup while a turn runs")
	}

	stopped := press(t, typed, "esc")
	if !stopped.slashOpen {
		t.Fatalf("the first Esc during a run should stop, not close the popup")
	}
	if stopped.notice == "" {
		t.Errorf("stopping should tell the operator what is happening")
	}

	stopped.running = false
	closed := press(t, stopped, "esc")
	if closed.slashOpen || len(closed.slashMatches) != 0 {
		t.Fatalf("the second Esc should close the popup")
	}
}

// TestSlashCursorResetsWhenMatchesChange proves Enter keeps working across
// edits: with the cursor parked on the back row of "/model" (one match) and
// the text changed to "/harness" (also one match), the highlight must return
// to the command instead of silently closing the menu on Enter.
func TestSlashCursorResetsWhenMatchesChange(t *testing.T) {
	model := chatModel(t)
	model = openSlashPalette(t, model)
	model.slashInput = "/model"
	model.refreshSlashMenu()
	if len(model.slashMatches) != 1 {
		t.Fatalf("the fixture needs one match, got %+v", model.slashMatches)
	}
	model.slashCursor = len(model.slashMatches)

	model.slashInput = "/harness"
	model.refreshSlashMenu()
	if model.slashCursor != 0 {
		t.Fatalf("a new match set should highlight the command, got cursor %d", model.slashCursor)
	}

	submitted := press(t, model, "enter")
	if submitted.current != modePicker {
		t.Fatalf("Enter on /harness should open its choices, mode is %d", submitted.current)
	}
	if submitted.picker.action != slashArgAction+"harness" {
		t.Fatalf("picker action = %q, want the harness menu", submitted.picker.action)
	}
}

// TestBareModeCommandsOpenTheirChoices is the requested behaviour: a bare
// /approval or /harness offers the modes instead of only printing the current
// one, and the picker can still run the bare form through its "(no argument)"
// row.
func TestBareModeCommandsOpenTheirChoices(t *testing.T) {
	for _, name := range []string{"approval", "harness"} {
		model := chatModel(t)
		model.composer.SetValue("/" + name)
		opened := press(t, model, "enter")

		if opened.current != modePicker {
			t.Fatalf("Enter on /%s should open the argument menu, mode is %d", name, opened.current)
		}
		if opened.picker.action != slashArgAction+name {
			t.Fatalf("picker action = %q, want the %s menu", opened.picker.action, name)
		}
		if got := opened.picker.items[0].ID; got != "" {
			t.Errorf("the %s menu should keep a bare-form row first, got id %q", name, got)
		}
	}
}

// TestChoosingApprovalModeAppliesIt proves the choice reaches the app rather
// than only being listed.
func TestChoosingApprovalModeAppliesIt(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("/approval")
	opened := press(t, model, "enter")

	for index, item := range opened.picker.visible {
		if item.ID == "plan" {
			opened.picker.cursor = index
		}
	}
	chosen := press(t, opened, "enter")

	if got := chosen.appConfig().ApprovalMode; got != config.ApprovalPlan {
		t.Errorf("approval mode = %q, want %q", got, config.ApprovalPlan)
	}
}

// TestSlashBackRowClosesWithoutRunning proves the last menu row is a way out:
// Enter or Tab there closes the palette and runs nothing.
func TestSlashBackRowClosesWithoutRunning(t *testing.T) {
	for _, keyName := range []string{"enter", "tab"} {
		model := chatModel(t)
		typed := openSlashPalette(t, model)
		typed.slashInput = "/stat"
		typed.refreshSlashMenu()
		if len(typed.slashMatches) == 0 {
			t.Fatalf("the menu should offer a completion for /stat")
		}
		typed.slashCursor = len(typed.slashMatches)

		updated := press(t, typed, keyName)
		if updated.slashOpen || len(updated.slashMatches) != 0 {
			t.Errorf("%s on back should close the menu", keyName)
		}
		if updated.running {
			t.Errorf("%s on back must not start a run", keyName)
		}
		if view := display(updated); strings.Contains(view, "workspace:") {
			t.Errorf("%s on back must not run the command:\n%s", keyName, view)
		}
	}
}

// TestEscClearsTypedSlashTextWithoutAMenu pins the rule that follows from the
// new input model: with no menu open, Esc clears the composer in one press,
// because a typed slash command is ordinary text until it is submitted.
func TestEscClearsTypedSlashTextWithoutAMenu(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("/stat")
	model.updateSlashMatches()
	if len(model.slashMatches) != 0 {
		t.Fatalf("a typed command must not open a menu")
	}

	cleared := press(t, model, "esc")
	if got := cleared.composer.Value(); got != "" {
		t.Errorf("Esc should clear the composer, got %q", got)
	}
}

// TestSlashMenuShowsTheWayBack keeps the affordance discoverable: the back
// row and its hint must render in the palette.
func TestSlashMenuShowsTheWayBack(t *testing.T) {
	model := chatModel(t)
	model = openSlashPalette(t, model)
	model.slashInput = "/mo"
	model.refreshSlashMenu()
	view := stripANSI(model.View())
	for _, want := range []string{"<- back", "Esc back"} {
		if !strings.Contains(view, want) {
			t.Errorf("the slash menu is missing %q:\n%s", want, view)
		}
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

// TestTabOnAnArgumentCommandOpensItsArguments covers the menu path. Tab is the
// documented way to complete a command, so on a command whose argument is a
// closed set it has to offer those values: completing to bare text left the
// operator to remember them, which made the menu look read-only.
func TestTabOnAnArgumentCommandOpensItsArguments(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("/trust")
	model.updateSlashMatches()
	model.slashCursor = 0

	opened := press(t, model, "tab")
	if opened.current != modePicker {
		t.Fatalf("Tab on /trust should open the argument menu, mode is %d", opened.current)
	}
	if opened.picker.action != "slash-arg:trust" {
		t.Fatalf("picker action = %q, want the trust argument menu", opened.picker.action)
	}
	values := map[string]bool{}
	for _, item := range opened.picker.items {
		values[item.ID] = true
	}
	for _, want := range []string{"", "on", "off"} {
		if !values[want] {
			t.Errorf("the argument menu is missing %q, has %v", want, values)
		}
	}
}

// TestPickingAnArgumentRunsTheCommand proves the chosen row reaches the
// command, so the value takes effect rather than only being printed.
func TestPickingAnArgumentRunsTheCommand(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("/trust")
	model.updateSlashMatches()

	opened := press(t, model, "tab")
	for index, item := range opened.picker.visible {
		if item.ID == "off" {
			opened.picker.cursor = index
		}
	}
	chosen := press(t, opened, "enter")

	if chosen.app.Trusted() {
		t.Errorf("choosing off should have untrusted the folder")
	}
	if chosen.current != modeChat {
		t.Errorf("choosing an argument should return to chat, mode is %d", chosen.current)
	}
	if got := chosen.composer.Value(); got != "" {
		t.Errorf("running an argument must clear the staged composer, got %q", got)
	}
}

// TestEnterAfterASpaceOpensTheArgumentMenu covers the typed route: "/approval "
// asks for the arguments, and the prefix menu has nothing to complete there.
func TestEnterAfterASpaceOpensTheArgumentMenu(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("/approval ")
	model.updateSlashMatches()

	opened := press(t, model, "enter")
	if opened.current != modePicker {
		t.Fatalf("Enter on \"/approval \" should open the argument menu, mode is %d", opened.current)
	}
	values := map[string]bool{}
	for _, item := range opened.picker.items {
		values[item.ID] = true
	}
	for _, want := range []string{"ask", "edits", "all", "plan"} {
		if !values[want] {
			t.Errorf("the argument menu is missing %q", want)
		}
	}
}

// TestChoosingAnIncompleteArgumentFillsTheComposer keeps a value that only
// starts an argument from being run on its own: /sessions delete needs an id,
// so choosing "delete" has to leave the operator typing.
func TestChoosingAnIncompleteArgumentFillsTheComposer(t *testing.T) {
	model := chatModel(t)
	before := len(model.blocks)
	model.composer.SetValue("/sessions")
	model.updateSlashMatches()

	opened := press(t, model, "tab")
	for index, item := range opened.picker.visible {
		if item.ID == "delete" {
			opened.picker.cursor = index
		}
	}
	chosen := press(t, opened, "enter")

	if chosen.current != modeChat {
		t.Fatalf("an incomplete argument should return to typing, mode is %d", chosen.current)
	}
	if got := chosen.composer.Value(); !strings.HasPrefix(got, "/sessions delete") {
		t.Errorf("the composer = %q, want the command and the chosen argument", got)
	}
	if len(chosen.blocks) != before {
		t.Errorf("nothing should have run, block count went from %d to %d", before, len(chosen.blocks))
	}
}

// TestHarnessIsOfferedAsAChoice verifies the one command whose options come
// from another catalogue rather than from a literal list.
func TestHarnessIsOfferedAsAChoice(t *testing.T) {
	options := SlashOptions("harness")
	if len(options) < 5 {
		t.Fatalf("only %d harness options", len(options))
	}
	seen := map[string]bool{}
	for _, option := range options {
		seen[option.Value] = true
	}
	if !seen[agent.DefaultHarnessProfile] {
		t.Errorf("the default profile %q should be offered", agent.DefaultHarnessProfile)
	}
}

// TestEveryArgumentCommandOffersItsDocumentedValues guards the catalogue: a
// command that names its arguments in Args but declares no options is one the
// menu cannot offer, which is the defect this menu fixes.
func TestEveryArgumentCommandOffersItsDocumentedValues(t *testing.T) {
	for _, command := range slashCommands {
		if !strings.Contains(command.Args, "|") {
			continue
		}
		name := strings.TrimPrefix(command.Trigger, "/")
		options := SlashOptions(name)
		if len(options) == 0 {
			t.Errorf("%s documents %q but offers no choices in the menu", command.Trigger, command.Args)
			continue
		}
		documented := strings.NewReplacer("[", "", "]", "").Replace(command.Args)
		for _, value := range strings.Split(documented, "|") {
			value = strings.TrimSpace(value)
			if value == "" || strings.HasPrefix(value, "<") {
				// An angle-bracketed token is free text the operator types,
				// so it has no fixed choice to offer in the menu.
				continue
			}
			found := false
			for _, option := range options {
				if option.Value == value {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s documents %q but the menu does not offer it", command.Trigger, value)
			}
		}
	}
}

// TestSlashCommandDuringATurnIsNotSentToTheModel is the regression this
// carve-out exists for. The operator typed /cost and /harness while a turn was
// running; both were handed to the model as steer prose, so the agent read the
// literal text "/cost" and the operator never saw the answer.
func TestSlashCommandDuringATurnIsNotSentToTheModel(t *testing.T) {
	for _, name := range []string{"/cost", "/harness", "/status", "/plan", "/tools"} {
		model := chatModel(t)
		model.running = true
		model.app.Steer("")   // ensure the steer queue starts empty
		model.app.TakeSteer() // drain it
		model.composer.SetValue(name)

		ran := press(t, model, "enter")

		if got := ran.app.SteerCount(); got != 0 {
			t.Errorf("%s was steered to the model instead of running: %d queued", name, got)
		}
		if len(ran.queue) != 0 {
			t.Errorf("%s should run at once, %d item(s) queued", name, len(ran.queue))
		}
		if len(ran.blocks) == 0 {
			t.Errorf("%s printed nothing", name)
		}
	}
}

// TestFreeTextDuringATurnStillSteers keeps the other half of the behaviour:
// prose typed mid-turn is a course correction and reaches the running agent.
func TestFreeTextDuringATurnStillSteers(t *testing.T) {
	if !shouldSteer("actually use the other parser", true) {
		t.Errorf("free text should steer a running turn")
	}
	if shouldSteer("actually use the other parser", false) {
		t.Errorf("free text should not steer when nothing is running")
	}
}

// TestSlashCommandNeverSteersToTheModel is the decision table itself. A command
// reaching the model as prose is the bug the operator hit.
func TestSlashCommandNeverSteersToTheModel(t *testing.T) {
	for _, command := range SlashCommands() {
		if shouldSteer(command.Trigger, true) {
			t.Errorf("%s would be sent to the model as prose", command.Trigger)
		}
		if shouldSteer(command.Trigger+" extra", true) {
			t.Errorf("%s with an argument would be sent to the model as prose", command.Trigger)
		}
	}
	// An unknown command is still a command: it must produce the unknown-command
	// message rather than becoming a prompt.
	if shouldSteer("/nonsense", true) {
		t.Errorf("an unknown command must be queued, not steered")
	}
}

// TestCommandWithWorkBehindItIsQueuedNotSteered covers a command that would
// start its own turn: /new replaces the session, so it waits for the drain
// instead of being sent to the model as prose.
func TestCommandWithWorkBehindItIsQueuedNotSteered(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.composer.SetValue("/new")

	queued := press(t, model, "enter")

	if got := queued.app.SteerCount(); got != 0 {
		t.Errorf("/new must not be steered to the model, SteerCount = %d", got)
	}
	if len(queued.queue) != 1 {
		t.Fatalf("queue = %d item(s), want /new waiting", len(queued.queue))
	}
	if queued.queue[0] != "/new" {
		t.Errorf("queued %q, want /new", queued.queue[0])
	}
}

// TestEveryCommandIsClassifiedAsLiveOrQueued guards the classification: a new
// command must be a deliberate choice, not an accidental steer to the model.
func TestEveryCommandIsClassifiedAsLiveOrQueued(t *testing.T) {
	live := map[string]bool{}
	for name := range liveSlashCommands {
		live[name] = true
	}
	for _, command := range SlashCommands() {
		name := strings.TrimPrefix(command.Trigger, "/")
		if live[name] {
			continue
		}
		// Not live means queued, which is a valid answer, but the set must not
		// silently accept a command that can only work at once.
		if name == "stop" || name == "exit" {
			t.Errorf("%s must be live: queueing it defers it past the moment it is needed", name)
		}
	}
}

// TestExitDuringATurnStillQuits pins the control that cannot wait: queueing
// /exit would leave the operator unable to quit.
func TestExitDuringATurnStillQuits(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.composer.SetValue("/exit")

	_, cmd := model.Update(key("enter"))
	if cmd == nil {
		t.Fatalf("/exit during a turn should still return the quit command")
	}
}

// TestUnknownSlashCommandNamesItself checks the failure path. A command that
// does not exist must say so and point at help rather than do nothing.
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

	// Assert on the transcript, not the viewport window: the catalogue is
	// longer than one screen, and the window only shows the tail.
	transcript := allText(final)
	for _, want := range []string{"read_file", "run_background", "git_commit", "edit"} {
		if !strings.Contains(transcript, want) {
			t.Errorf("/tools should list %s:\n%s", want, transcript)
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
	application := testApp(t)
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

// TestSlashQueuesBehindARun pins the one-turn rule: a command that starts its
// own turn waits in the queue instead of starting a second one.
//
// /new is used rather than /status: a read-only command deliberately runs at
// once during a turn, so queueing is only the rule for work that would collide.
func TestSlashQueuesBehindARun(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.composer.SetValue("/new")

	queued := press(t, model, "enter")
	if len(queued.queue) != 1 || queued.queue[0] != "/new" {
		t.Fatalf("queue = %q, want the command held", queued.queue)
	}
}

// TestReadOnlyCommandRunsDuringARun is the other half, and the bug the
// operator reported: /cost and /harness typed mid-turn were handed to the model
// as prose, so the operator never saw the answer.
func TestReadOnlyCommandRunsDuringARun(t *testing.T) {
	for _, command := range []string{"/cost", "/harness", "/status", "/plan"} {
		model := chatModel(t)
		model.running = true
		model.composer.SetValue(command)

		ran := press(t, model, "enter")
		if len(ran.queue) != 0 {
			t.Errorf("%s should run at once, got queue %q", command, ran.queue)
		}
		if ran.app.SteerCount() != 0 {
			t.Errorf("%s was steered to the model", command)
		}
		if view := display(ran); strings.TrimSpace(view) == "" {
			t.Errorf("%s printed nothing", command)
		}
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

// TestComposerTakesFiveRows pins the roomier input: the transcript keeps the
// rest of the window, derived from the composer height rather than a magic
// number.
func TestComposerTakesFiveRows(t *testing.T) {
	model := chatModel(t)
	if model.viewport.Height != 40-(composerHeight+6) {
		t.Errorf("viewport height = %d, want %d", model.viewport.Height, 40-(composerHeight+6))
	}
	if composerHeight != 5 {
		t.Errorf("composer height = %d, want the roomier 5 rows", composerHeight)
	}
}

// scrollFixture builds a transcript taller than the viewport so scrolling
// has somewhere to go.
func scrollFixture(model *Model) *Model {
	for index := 0; index < 60; index++ {
		model.blocks = append(model.blocks, block{kind: blockAssistant, text: strings.Repeat("line ", 20) + string(rune('a'+index%26))})
	}
	model.refresh()
	return model
}

// TestScrollUpHoldsPositionAcrossRefresh proves the follow rule: new output
// while reading history must not yank the view to the bottom.
func TestScrollUpHoldsPositionAcrossRefresh(t *testing.T) {
	model := scrollFixture(chatModel(t))
	if !model.viewport.AtBottom() {
		t.Fatalf("a fresh transcript should follow the bottom")
	}

	moved := press(t, model, "pgup")
	if moved.viewport.AtBottom() {
		t.Fatalf("PgUp should leave the bottom")
	}
	held := moved.viewport.YOffset

	moved.blocks = append(moved.blocks, block{kind: blockAssistant, text: "fresh output"})
	moved.refresh()
	if moved.viewport.YOffset != held {
		t.Errorf("offset = %d, want the held %d", moved.viewport.YOffset, held)
	}
	if view := display(moved); !strings.Contains(view, "scrolled (End follows)") {
		t.Errorf("the status should name the way back:\n%s", view)
	}
}

// TestEndResumesFollowing proves the way back: End returns to the bottom and
// later output follows again.
func TestEndResumesFollowing(t *testing.T) {
	model := scrollFixture(chatModel(t))
	moved := press(t, model, "pgup")
	followed := press(t, moved, "end")
	if !followed.viewport.AtBottom() {
		t.Fatalf("End should return to the bottom")
	}
	followed.blocks = append(followed.blocks, block{kind: blockAssistant, text: "more"})
	followed.refresh()
	if !followed.viewport.AtBottom() {
		t.Errorf("output at the bottom should keep following")
	}
}

// TestMouseWheelScrollsTheTranscript proves the wheel path: wheel messages
// reach the viewport instead of dying in the composer.
func TestMouseWheelScrollsTheTranscript(t *testing.T) {
	model := scrollFixture(chatModel(t))
	before := model.viewport.YOffset
	next, _ := model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	moved := next.(*Model)
	if moved.viewport.YOffset >= before {
		t.Errorf("wheel up should scroll, offset %d did not drop below %d", moved.viewport.YOffset, before)
	}
}

// TestWindowTitleMirrorsTheRunState pins the tab title: idle names the
// workspace folder, working names the elapsed turn, approval asks for help.
func TestWindowTitleMirrorsTheRunState(t *testing.T) {
	model := chatModel(t)
	if got := model.windowTitle(); !strings.Contains(got, "Termixgo") || strings.Contains(got, "working") {
		t.Errorf("idle title = %q", got)
	}

	model.running = true
	model.runStarted = time.Now()
	if got := model.windowTitle(); !strings.Contains(got, "working") {
		t.Errorf("working title = %q", got)
	}

	model.pendingApproval = &agent.ApprovalRequest{Tool: "run_command"}
	if got := model.windowTitle(); !strings.Contains(got, "approval needed") {
		t.Errorf("approval title = %q", got)
	}
}

// TestStatusBarAnimatesWhileWorking keeps the working indicator visible even
// when a menu replaces the hints row.
func TestStatusBarAnimatesWhileWorking(t *testing.T) {
	model := chatModel(t)
	model.running = true
	model.runStarted = time.Now()
	if view := display(model); !strings.Contains(view, "working") {
		t.Errorf("the status bar should carry the working state:\n%s", view)
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
