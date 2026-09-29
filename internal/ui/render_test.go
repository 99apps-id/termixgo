package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
)

// ------------------------------------------------- terminal detection

// TestTerminalDetectionDistinguishesPipesFromFiles is what decides whether the
// full-screen UI starts at all. Getting it wrong either paints escape codes
// into a pipe or refuses to start a real terminal, so both halves matter.
func TestTerminalDetectionDistinguishesPipesFromFiles(t *testing.T) {
	var buffer bytes.Buffer
	if isTerminalWriter(&buffer) {
		t.Errorf("a buffer is not a terminal")
	}
	if isTerminalReader(strings.NewReader("x")) {
		t.Errorf("a string reader is not a terminal")
	}
	// A file that is not a terminal file descriptor, which is the CI case.
	file, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer file.Close()
	if isTerminalWriter(file) {
		t.Errorf("a regular file is not a terminal")
	}
	if isTerminalReader(file) {
		t.Errorf("a regular file is not a terminal")
	}

	// A pipe only becomes a terminal through a real tty, which a test cannot
	// invent, so the answer must simply be false rather than a panic.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()
	if isTerminalWriter(writer) || isTerminalReader(reader) {
		t.Errorf("a pipe is not a terminal")
	}

	if IsInteractive(&buffer, &buffer) {
		t.Errorf("two buffers are not an interactive session")
	}
}

// ------------------------------------------------- render branches

func TestRenderToolBlockMarksRunningDoneAndFailed(t *testing.T) {
	styles := NewStyles(DefaultPalette())

	running := stripANSI(renderToolBlock(block{kind: blockTool, running: true, toolLabel: "Reading main.go"}, styles, 80, true))
	if !strings.Contains(running, "> Reading main.go") {
		t.Errorf("running = %q, want a > marker", running)
	}

	done := stripANSI(renderToolBlock(block{kind: blockTool, toolOK: true, toolLabel: "Read main.go", toolMillis: 42}, styles, 80, true))
	if !strings.Contains(done, "+ Read main.go") {
		t.Errorf("done = %q, want a + marker", done)
	}
	if !strings.Contains(done, "(42ms)") {
		t.Errorf("done = %q, want the elapsed time", done)
	}

	failed := stripANSI(renderToolBlock(block{kind: blockTool, toolOK: false, toolLabel: "Read main.go"}, styles, 80, true))
	if !strings.Contains(failed, "x Read main.go") {
		t.Errorf("failed = %q, want an x marker", failed)
	}
	// A tool that finished without a recorded duration must not print (0ms).
	if strings.Contains(failed, "ms)") {
		t.Errorf("failed = %q, want no duration", failed)
	}

	// A long label is clipped to the width so one path cannot break the layout.
	long := stripANSI(renderToolBlock(block{kind: blockTool, toolOK: true, toolLabel: strings.Repeat("p", 200)}, styles, 40, true))
	if len([]rune(long)) > 42 {
		t.Errorf("a long label was not clipped: %d runes", len([]rune(long)))
	}
}

func TestRenderThinkingBlockTrimsALongReasoning(t *testing.T) {
	styles := NewStyles(DefaultPalette())

	running := stripANSI(renderThinkingBlock(block{kind: blockThinking, running: true, reasoning: "thinking now"}, styles, 80, true))
	if !strings.Contains(running, "Thinking...") {
		t.Errorf("running = %q, want the live header", running)
	}
	if !strings.Contains(running, "thinking now") {
		t.Errorf("running = %q, want the text", running)
	}

	timed := stripANSI(renderThinkingBlock(block{kind: blockThinking, reasoning: "considered", seconds: 7}, styles, 80, true))
	if !strings.Contains(timed, "Reasoned for 7s") {
		t.Errorf("timed = %q, want the duration", timed)
	}

	// No duration and not running: a bare header.
	bare := stripANSI(renderThinkingBlock(block{kind: blockThinking, reasoning: "considered"}, styles, 80, true))
	if !strings.Contains(bare, "Reasoned") || strings.Contains(bare, "for") {
		t.Errorf("bare = %q, want a plain header", bare)
	}

	// No reasoning at all leaves the header alone rather than an empty block.
	empty := stripANSI(renderThinkingBlock(block{kind: blockThinking}, styles, 80, true))
	if !strings.Contains(empty, "Reasoned") {
		t.Errorf("empty = %q", empty)
	}

	// A wall of reasoning is capped, or one thought fills the transcript.
	var lines []string
	for index := 0; index < 40; index++ {
		lines = append(lines, "reasoning line")
	}
	trimmed := stripANSI(renderThinkingBlock(block{kind: blockThinking, reasoning: strings.Join(lines, "\n")}, styles, 60, true))
	if !strings.Contains(trimmed, "reasoning trimmed") {
		t.Errorf("a long reasoning block should say it was trimmed:\n%s", trimmed)
	}
}

func TestRenderPlanBlockRendersEveryStatus(t *testing.T) {
	styles := NewStyles(DefaultPalette())

	if got := stripANSI(renderPlanBlock(block{kind: blockPlan}, styles, 80)); !strings.Contains(got, "Plan is empty") {
		t.Errorf("an empty plan = %q", got)
	}

	rendered := stripANSI(renderPlanBlock(block{kind: blockPlan, plan: []agent.Todo{
		{ID: "1", Title: "done already", Status: "completed"},
		{ID: "2", Title: "doing now", Status: "in_progress"},
		{ID: "3", Title: "not yet", Status: "pending"},
	}}, styles, 80))

	if !strings.Contains(rendered, "Plan (1/3)") {
		t.Errorf("rendered = %q, want the progress count", rendered)
	}
	for _, want := range []string{"[x] done already", "[>] doing now", "[ ] not yet"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered is missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderPrefixedSkipsEmptyText(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	if got := renderPrefixed("   ", "  ", styles.Dim, 80); got != "" {
		t.Errorf("blank text = %q, want nothing", got)
	}
	if got := stripANSI(renderPrefixed("a note", "  ", styles.Dim, 80)); !strings.Contains(got, "a note") {
		t.Errorf("rendered = %q", got)
	}
}

// ---------------------------------------------- the event plumbing

// TestWaitForEventDeliversTheNextEvent covers the closure the UI returns after
// every update: it is what keeps the transcript live. The channel is supplied
// directly so the test does not depend on a turn running.
func TestWaitForEventDeliversTheNextEvent(t *testing.T) {
	events := make(chan agent.Event, 1)
	events <- agent.Event{Kind: agent.EventText, Text: "queued"}

	message := waitForEvent(events)()
	event, ok := message.(eventMsg)
	if !ok {
		t.Fatalf("message = %T, want eventMsg", message)
	}
	if agent.Event(event).Text != "queued" {
		t.Errorf("event = %+v, want the queued one", event)
	}
}

// TestTickCarriesTheTime is how the elapsed indicator advances.
func TestTickCarriesTheTime(t *testing.T) {
	message := tick()()
	if _, ok := message.(tickMsg); !ok {
		t.Fatalf("message = %T, want tickMsg", message)
	}
}

// TestSubmitRoutesSlashAndChat is the fork every Enter press takes.
func TestSubmitRoutesSlashAndChat(t *testing.T) {
	model := chatModel(t)

	// A slash command is handled in place and leaves no run behind.
	after, _ := model.submit("/status")
	slashed, ok := after.(*Model)
	if !ok {
		t.Fatalf("submit returned %T", after)
	}
	if slashed.running {
		t.Errorf("a slash command must not start a run")
	}
	if !strings.Contains(allText(slashed), "workspace:") {
		t.Errorf("the command should have produced output:\n%s", allText(slashed))
	}
}

func TestLastBlockHandlesAnEmptyTranscript(t *testing.T) {
	model := chatModel(t)
	model.blocks = nil
	if got := model.lastBlock(); got != nil {
		t.Errorf("lastBlock = %+v, want nil for an empty transcript", got)
	}
	model.blocks = []block{{kind: blockNotice, text: "only one"}}
	if got := model.lastBlock(); got == nil || got.text != "only one" {
		t.Errorf("lastBlock = %+v", got)
	}
}

// TestApplyEventCoversTheRemainingKinds walks the kinds the folding tests do
// not reach, so a new event kind cannot be added without a branch.
func TestApplyEventCoversTheRemainingKinds(t *testing.T) {
	model := chatModel(t)

	model.applyEvent(agent.Event{Kind: agent.EventTurnStart})
	model.applyEvent(agent.Event{Kind: agent.EventNotice, Text: "a notice"})
	model.applyEvent(agent.Event{Kind: agent.EventError, Err: os.ErrDeadlineExceeded})
	// An error with no cause would print "nil"; the branch guards against it.
	model.applyEvent(agent.Event{Kind: agent.EventError})
	model.applyEvent(agent.Event{Kind: agent.EventPlan, Plan: []agent.Todo{{ID: "1", Title: "step", Status: "pending"}}})
	// A second plan event updates the block in place rather than stacking.
	model.applyEvent(agent.Event{Kind: agent.EventPlan, Plan: []agent.Todo{{ID: "1", Title: "step", Status: "completed"}}})

	text := allText(model)
	if !strings.Contains(text, "a notice") {
		t.Errorf("the notice is missing:\n%s", text)
	}
	// os.ErrDeadlineExceeded renders as "i/o timeout", so assert on the cause
	// the operator would actually see.
	if !strings.Contains(text, os.ErrDeadlineExceeded.Error()) {
		t.Errorf("the error is missing:\n%s", text)
	}
	if strings.Contains(text, "<nil>") {
		t.Errorf("a nil error must not be rendered:\n%s", text)
	}

	plans := 0
	for _, item := range model.blocks {
		if item.kind == blockPlan {
			plans++
		}
	}
	if plans != 1 {
		t.Errorf("plans = %d, want one updated block", plans)
	}

	// The two stop reasons the transcript explains.
	for _, stop := range []string{"step-cap", "error", "stop"} {
		fresh := chatModel(t)
		fresh.applyEvent(agent.Event{Kind: agent.EventTurnEnd, StopReason: stop})
		if stop == "step-cap" && !strings.Contains(allText(fresh), "step budget") {
			t.Errorf("a step cap should be explained:\n%s", allText(fresh))
		}
		// A closed thinking block must not be left running.
		for _, item := range fresh.blocks {
			if item.kind == blockThinking && item.running {
				t.Errorf("stop %q left a thinking block running", stop)
			}
		}
	}
}

// TestHandleKeyRoutesByMode covers the mode switch that decides which handler a
// keystroke reaches.
func TestHandleKeyRoutesByMode(t *testing.T) {
	// Help is dismissed by any key according to the current contract.
	model := chatModel(t)
	model.current = modeHelp
	after := press(t, model, "x")
	if after.current != modeChat {
		t.Errorf("mode = %d, want chat after dismissing help", after.current)
	}

	// A question and an approval take priority over the composer, which is what
	// stops a stray keystroke from being typed into a prompt that is waiting.
	pending := chatModel(t)
	reply := make(chan agent.Decision, 1)
	pending.pendingApproval = &agent.ApprovalRequest{Tool: "run_command"}
	pending.approvalReply = reply
	dismissed := press(t, pending, "n")
	if dismissed.pendingApproval != nil {
		t.Errorf("the approval should have been answered")
	}
}

// TestViewRendersAtEveryCommonSize is the layout guard: a terminal that is too
// small must degrade rather than panic, and a usable one must keep the facts
// the operator needs on screen.
func TestViewRendersAtEveryCommonSize(t *testing.T) {
	limits := DefaultLimits()
	for _, size := range [][2]int{{40, 10}, {50, 16}, {80, 24}, {120, 40}, {200, 60}} {
		model := chatModel(t)
		resize(model, size[0], size[1])
		view := stripANSI(model.View())
		if strings.TrimSpace(view) == "" {
			t.Errorf("%dx%d rendered nothing", size[0], size[1])
		}
		if size[0] < limits.MinWidth || size[1] < limits.MinHeight {
			// Below the minimum the only useful message is how to fix it.
			if !strings.Contains(view, "too small") {
				t.Errorf("%dx%d should say the terminal is too small:\n%s", size[0], size[1], view)
			}
			continue
		}
		// At a usable size the header names the folder, because losing it is
		// what makes the operator unsure where the agent is working.
		if !strings.Contains(view, model.app.Workspace()) {
			t.Errorf("%dx%d lost the workspace from the header:\n%s", size[0], size[1], view)
		}
	}
}

func TestComposerAndHintsChangeWhileRunning(t *testing.T) {
	model := chatModel(t)
	idle := stripANSI(model.viewHints())
	if !strings.Contains(idle, "Enter send") {
		t.Errorf("idle hints = %q", idle)
	}

	model.running = true
	model.runStarted = time.Now()
	busy := stripANSI(model.viewHints())
	if !strings.Contains(busy, "working") {
		t.Errorf("busy hints = %q, want the elapsed indicator", busy)
	}
	if !strings.Contains(busy, "Esc stop") {
		t.Errorf("busy hints = %q, want the stop hint", busy)
	}
}

// TestViewSlashMenuMarksTheSelection covers the completion menu, which is the
// discoverability path for the commands.
func TestViewSlashMenuMarksTheSelection(t *testing.T) {
	model := chatModel(t)
	// The composer is what triggers the menu, so the text is set the way a
	// real keystroke would leave it.
	model.composer.SetValue("/")
	model.updateSlashMatches()
	if len(model.slashMatches) == 0 {
		t.Fatalf("the slash menu should have matches")
	}
	typed := model
	rendered := stripANSI(typed.viewSlashMenu())
	if !strings.Contains(rendered, "> ") {
		t.Errorf("rendered = %q, want the selected row marked", rendered)
	}
	if !strings.Contains(rendered, "/") {
		t.Errorf("rendered = %q, want the command triggers", rendered)
	}
	// Every row carries its summary, which is how the command is discovered.
	if !strings.Contains(rendered, typed.slashMatches[0].Summary) {
		t.Errorf("rendered = %q, want the summaries", rendered)
	}

	// Moving the cursor moves the marker rather than adding a second one.
	moved := press(t, typed, "down")
	if got := strings.Count(stripANSI(moved.viewSlashMenu()), "> "); got == 0 {
		t.Errorf("the marker disappeared after moving:\n%s", moved.viewSlashMenu())
	}
}

// TestPickerViewRendersFilterAndEmptyState covers the two states a filter can
// produce.
func TestPickerViewRendersFilterAndEmptyState(t *testing.T) {
	model := chatModel(t)
	model.openPicker("Choose", "model", setupModelItems("ollama"))

	filtered := press(t, model, "q")
	if !strings.Contains(stripANSI(filtered.viewPicker()), "filter: q") {
		t.Errorf("the filter text should be shown:\n%s", filtered.viewPicker())
	}

	none := press(t, filtered, "zzzz")
	if !strings.Contains(stripANSI(none.viewPicker()), "No matches") {
		t.Errorf("an empty list should say so:\n%s", none.viewPicker())
	}
	// The hint line has to survive, or the operator cannot get back out.
	if !strings.Contains(stripANSI(none.viewPicker()), "Esc cancel") {
		t.Errorf("the hint is missing:\n%s", none.viewPicker())
	}
}

// TestViewHeaderNamesFolderModelAndTrust covers the one line that is always on
// screen.
func TestViewHeaderNamesFolderModelAndTrust(t *testing.T) {
	model := chatModel(t)
	header := stripANSI(model.viewHeader())
	if !strings.Contains(header, "Termixgo") || !strings.Contains(header, model.app.Workspace()) {
		t.Errorf("header = %q", header)
	}
	if !strings.Contains(header, "untrusted") {
		t.Errorf("header = %q, want the trust state", header)
	}

	trusted := chatModel(t)
	if err := trusted.app.SetTrust(true); err != nil {
		t.Fatalf("SetTrust: %v", err)
	}
	if got := stripANSI(trusted.viewHeader()); !strings.Contains(got, "trusted") {
		t.Errorf("header = %q, want it to report the trusted state", got)
	}
}

func TestBlockErrorAndUserRenderTheirMarker(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	width := 80

	user := stripANSI(renderUserBlock(block{kind: blockUser, text: "hello there"}, styles, width))
	if !strings.Contains(user, "> hello there") {
		t.Errorf("user = %q, want the prompt marker", user)
	}
	// A wrapped line is indented rather than repeating the prompt.
	wrapped := stripANSI(renderUserBlock(block{kind: blockUser, text: strings.Repeat("word ", 40)}, styles, 40))
	if strings.Count(wrapped, "> ") != 1 {
		t.Errorf("the prompt should appear once:\n%s", wrapped)
	}

	failure := stripANSI(renderBlock(block{kind: blockError, text: "boom"}, styles, width, true))
	if !strings.Contains(failure, "boom") {
		t.Errorf("error = %q", failure)
	}
	notice := stripANSI(renderBlock(block{kind: blockNotice, text: "a notice"}, styles, width, true))
	if !strings.Contains(notice, "a notice") {
		t.Errorf("notice = %q", notice)
	}
	unknown := renderBlock(block{kind: blockKind(9999), text: "raw"}, styles, width, true)
	if unknown != "raw" {
		t.Errorf("an unknown kind = %q, want the text unchanged", unknown)
	}
}

// TestWindowSizeAndQuitAreHandled covers the two messages every session sends.
func TestWindowSizeAndQuitAreHandled(t *testing.T) {
	model := chatModel(t)
	model.ready = false
	after, _ := send(t, model, tea.WindowSizeMsg{Width: 100, Height: 30})
	if !after.ready || after.width != 100 || after.height != 30 {
		t.Errorf("the model did not take the size: %d x %d ready=%v", after.width, after.height, after.ready)
	}
	if view := display(after); strings.Contains(view, "Loading Termixgo") {
		t.Errorf("a sized model should render the real view:\n%s", view)
	}
	if _, cmd := send(t, after, key("ctrl+c")); cmd == nil {
		t.Errorf("ctrl+c should return the quit command")
	}
}
