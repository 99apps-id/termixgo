package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

func TestHistoryWalksAndRestoresTheDraft(t *testing.T) {
	model := chatModel(t)
	model.history = &promptHistory{}
	model.history.add("first prompt")
	model.history.add("second prompt")
	model.composer.SetValue("half typed")

	up := press(t, model, "up")
	if got := up.composer.Value(); got != "second prompt" {
		t.Fatalf("Up should recall the newest entry, got %q", got)
	}
	up = press(t, up, "up")
	if got := up.composer.Value(); got != "first prompt" {
		t.Fatalf("a second Up should go further back, got %q", got)
	}
	down := press(t, press(t, up, "down"), "down")
	if got := down.composer.Value(); got != "half typed" {
		t.Fatalf("walking down past the newest entry restores the draft, got %q", got)
	}
}

func TestHistoryPersistsAndSkipsSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	history := loadHistory(path)
	history.add("explain the parser")
	history.add("/endpoint add gw https://x/v1 sk-secret")
	history.add("explain the parser")
	reloaded := loadHistory(path)
	if len(reloaded.entries) != 1 || reloaded.entries[0] != "explain the parser" {
		t.Fatalf("entries = %q", reloaded.entries)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "sk-secret") {
		t.Fatalf("a command that can carry a key must never be written")
	}
}

func TestHistoryLeavesMultiLineEditingAlone(t *testing.T) {
	model := chatModel(t)
	model.history = &promptHistory{}
	model.history.add("old")
	model.composer.SetValue("line one\nline two")
	// The cursor is on the last line, so Up moves inside the draft.
	moved := press(t, model, "up")
	if got := moved.composer.Value(); got != "line one\nline two" {
		t.Fatalf("Up inside a multi-line draft must not recall history, got %q", got)
	}
}

func TestShiftTabCyclesTheApprovalMode(t *testing.T) {
	model := chatModel(t)
	_ = model.app.SetApprovalMode(config.ApprovalAsk)
	want := []config.ApprovalMode{config.ApprovalEdits, config.ApprovalAll, config.ApprovalPlan, config.ApprovalAsk}
	for _, mode := range want {
		next, _ := model.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
		model = next.(*Model)
		if got := model.app.Config().ApprovalMode; got != mode {
			t.Fatalf("mode = %s, want %s", got, mode)
		}
	}
}

func TestInlineCtrlOOpensTheTranscriptPager(t *testing.T) {
	model := chatModel(t)
	model.inline = true
	model.blocks = append(model.blocks, block{kind: blockThinking, reasoning: "hidden reasoning", finalized: true})
	next, cmd := model.Update(key("ctrl+o"))
	pager := next.(*Model)
	if pager.current != modeTranscript || cmd == nil {
		t.Fatalf("Ctrl+O should open the pager on the alternate screen")
	}
	if view := stripANSI(pager.View()); !strings.Contains(view, "hidden reasoning") || !strings.Contains(view, "Transcript") {
		t.Fatalf("the pager shows every detail:\n%s", view)
	}
	pager.pendingPrint = nil
	pager.blocks = append(pager.blocks, block{kind: blockNotice, text: "arrives while reading"})
	pager.refresh()
	if len(pager.pendingPrint) != 0 {
		t.Fatalf("nothing may print while the pager holds the screen")
	}
	if pager.printed == len(pager.blocks) {
		t.Fatalf("the new block must still be waiting")
	}
	closed, closeCmd := pager.Update(key("esc"))
	back := closed.(*Model)
	if back.current != modeChat || closeCmd == nil {
		t.Fatalf("Esc should close the pager and leave the alternate screen")
	}
	if back.printed != len(back.blocks) {
		t.Fatalf("what arrived while reading should print once the pager closes: %d of %d", back.printed, len(back.blocks))
	}
}

func TestTurnSummaryNamesStepsCacheAndTime(t *testing.T) {
	model := chatModel(t)
	model.beginTurnStats()
	model.applyEvent(agent.Event{Kind: agent.EventContext, Usage: provider.Usage{PromptTokens: 1}})
	model.applyEvent(agent.Event{Kind: agent.EventContext, Usage: provider.Usage{PromptTokens: 1}})
	model.app.Emit(agent.Event{Kind: agent.EventUsage, Usage: provider.Usage{PromptTokens: 20000, CacheReadTokens: 18000, CompletionTokens: 900, TotalTokens: 20900}})
	summary := model.turnSummary(83 * time.Second)
	for _, want := range []string{"2 step(s)", "20.0k in (90% cache)", "900 out", "1m23s"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q lacks %q", summary, want)
		}
	}
}

func TestNewCommands(t *testing.T) {
	model := chatModel(t)
	for _, tc := range []struct {
		command, args, want string
	}{
		{"context", "", "system prompt"},
		{"permissions", "", "Always allowed:"},
		{"rename", "Parser refactor", "Renamed this session to Parser refactor."},
		{"layout", "", "Layout is"},
	} {
		next, _ := model.runSlash(tc.command, tc.args)
		model = next.(*Model)
		if view := display(model); !strings.Contains(view, tc.want) {
			t.Fatalf("/%s should print %q:\n%s", tc.command, tc.want, view)
		}
	}
	if model.app.Session().Title() != "Parser refactor" {
		t.Fatalf("/rename should set the title")
	}

	model.app.AllowTool("run_command")
	next, _ := model.runSlash("permissions", "revoke run_command")
	model = next.(*Model)
	for _, name := range model.app.Permissions().Always {
		if name == "run_command" {
			t.Fatalf("revoke should drop the allowance")
		}
	}

	switched, cmd := model.runSlash("layout", "inline")
	if !switched.(*Model).inline || cmd == nil || model.app.Config().TUILayout != "inline" {
		t.Fatalf("/layout inline should switch and persist")
	}
}

func TestCtrlRReverseHistorySearchFiltersCyclesAndCancels(t *testing.T) {
	model := chatModel(t)
	model.history = &promptHistory{}
	model.history.add("fix login bug")
	model.history.add("refactor parser")
	model.history.add("fix websocket bug")
	model.composer.SetValue("unsent draft")

	next, _ := model.Update(key("ctrl+r"))
	m := next.(*Model)
	if !m.historySearchActive {
		t.Fatalf("Ctrl+R should activate reverse history search")
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "reverse-i-search") {
		t.Fatalf("view should show reverse-i-search bar:\n%s", view)
	}

	for _, r := range "fix" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(*Model)
	}
	matches := m.historySearchMatches()
	if len(matches) != 2 || matches[0] != "fix websocket bug" || matches[1] != "fix login bug" {
		t.Fatalf("matches = %q, want newest-first [fix websocket bug, fix login bug]", matches)
	}

	// Ctrl+R advances to the older match; Tab loads it into the composer.
	next, _ = m.Update(key("ctrl+r"))
	m = next.(*Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(*Model)
	if m.historySearchActive {
		t.Fatalf("Tab should close search")
	}
	if got := m.composer.Value(); got != "fix login bug" {
		t.Fatalf("composer = %q, want %q", got, "fix login bug")
	}

	// Re-open search and press Esc to restore the current draft.
	next, _ = m.Update(key("ctrl+r"))
	m = next.(*Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("parser")})
	m = next.(*Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = next.(*Model)
	if m.historySearchActive || m.composer.Value() != "fix login bug" {
		t.Fatalf("Esc should cancel search and restore draft, active=%v composer=%q", m.historySearchActive, m.composer.Value())
	}
}

func TestEscEscOnEmptyComposerOpensRewindAndEditsLastTurn(t *testing.T) {
	model := chatModel(t)
	model.app.Session().AddUser("first prompt")
	model.app.Session().AddAssistant("first reply", "", nil, nil)
	model.app.Session().AddUser("second prompt with typo")
	model.app.Session().AddAssistant("second reply", "", nil, nil)
	model.blocks = append(model.blocks,
		block{kind: blockUser, text: "first prompt"},
		block{kind: blockAssistant, text: "first reply"},
		block{kind: blockUser, text: "second prompt with typo"},
		block{kind: blockAssistant, text: "second reply"},
	)

	// First Escape on empty composer arms rewind without opening picker.
	next, _ := send(t, model, tea.KeyMsg{Type: tea.KeyEscape})
	next, _ = send(t, next, settleEscMsg{})
	if next.escRewindArmedAt.IsZero() {
		t.Fatalf("first Esc on empty composer should arm rewind")
	}
	if next.current != modeChat {
		t.Fatalf("first Esc must not open picker yet")
	}

	// Second Escape opens the rewind picker.
	next, _ = send(t, next, tea.KeyMsg{Type: tea.KeyEscape})
	next, _ = send(t, next, settleEscMsg{})
	if next.current != modePicker || next.picker.action != "rewind" {
		t.Fatalf("second Esc should open rewind picker, got current=%d action=%q", next.current, next.picker.action)
	}

	// Selecting the default first item ("edit") pops the last turn and fills the composer.
	after, _ := next.Update(tea.KeyMsg{Type: tea.KeyEnter})
	edited := after.(*Model)
	if edited.current != modeChat {
		t.Fatalf("Enter should return to chat")
	}
	if got := edited.composer.Value(); got != "second prompt with typo" {
		t.Fatalf("composer = %q, want popped prompt", got)
	}
	prompt, ok := edited.app.Session().LastUserPrompt()
	if !ok || prompt != "first prompt" {
		t.Fatalf("LastUserPrompt = %q (%v), want %q", prompt, ok, "first prompt")
	}
}

func TestCompactTranscriptGroupsConsecutiveReadSearchTools(t *testing.T) {
	styles := NewStyles(DefaultPalette())
	blocks := []block{
		{kind: blockUser, text: "inspect repo"},
		{kind: blockTool, toolName: "read_file", toolLabel: "Read a.go", toolOK: true},
		{kind: blockTool, toolName: "read_file", toolLabel: "Read b.go", toolOK: true},
		{kind: blockTool, toolName: "grep", toolLabel: "Searched foo", toolOK: true},
		{kind: blockAssistant, text: "Found it."},
	}
	compact := stripANSI(transcript(blocks, styles, 80, false))
	if !strings.Contains(compact, "Read 2 files, searched 1 pattern"+expandHint()) {
		t.Fatalf("compact transcript should group consecutive read/search tools:\n%s", compact)
	}
	if strings.Contains(compact, "Read a.go") {
		t.Fatalf("grouped compact transcript should collapse individual read lines:\n%s", compact)
	}
	expanded := stripANSI(transcript(blocks, styles, 80, true))
	if !strings.Contains(expanded, "Read a.go") || !strings.Contains(expanded, "Read b.go") {
		t.Fatalf("expanded transcript should show every individual tool:\n%s", expanded)
	}
}

func TestInlinePinsLivePlanDuringRun(t *testing.T) {
	model := inlineModel(t)
	model.pendingPrint = nil
	model.running = true
	model.blocks = append(model.blocks, block{kind: blockUser, text: "build feature"})
	todos := []agent.Todo{
		{ID: "1", Title: "Read files", Status: "completed"},
		{ID: "2", Title: "Edit handler", Status: "in_progress"},
	}
	model.app.Todos().Set(todos)
	model.applyEvent(agent.Event{Kind: agent.EventPlan, Plan: todos})
	model.applyEvent(agent.Event{Kind: agent.EventToolStart, ToolName: "edit", ToolLabel: "Editing handler.go"})
	model.refresh()

	for _, printed := range model.pendingPrint {
		if strings.Contains(stripANSI(printed), "Edit handler") {
			t.Fatalf("live plan must stay pinned instead of printing mid-turn:\n%s", printed)
		}
	}
	if view := stripANSI(model.View()); !strings.Contains(view, "Edit handler") {
		t.Fatalf("pinned live plan should appear in inline view:\n%s", view)
	}
}

func TestTranscriptSearchFiltersAndClears(t *testing.T) {
	model := chatModel(t)
	model.inline = true
	model.blocks = append(model.blocks,
		block{kind: blockUser, text: "check database"},
		block{kind: blockTool, toolName: "run_command", toolLabel: "Ran sql", toolOK: true, toolOutput: "unique_needle_token\nother_line"},
	)
	next, _ := model.runSlash("transcript", "")
	pager := next.(*Model)
	if pager.current != modeTranscript {
		t.Fatalf("/transcript should open transcript viewer")
	}

	next, _ = pager.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	pager = next.(*Model)
	for _, r := range "unique_needle" {
		next, _ = pager.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		pager = next.(*Model)
	}
	next, _ = pager.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pager = next.(*Model)
	view := stripANSI(pager.View())
	if !strings.Contains(view, "unique_needle_token") || strings.Contains(view, "other_line") {
		t.Fatalf("filtered transcript should show matching lines only:\n%s", view)
	}

	// First Esc clears the active filter without closing the viewer.
	next, _ = pager.Update(tea.KeyMsg{Type: tea.KeyEscape})
	pager = next.(*Model)
	if pager.current != modeTranscript || pager.transcriptSearchQuery != "" {
		t.Fatalf("Esc on active filter should clear filter and stay in transcript")
	}
	// Second Esc closes the transcript viewer.
	next, _ = pager.Update(tea.KeyMsg{Type: tea.KeyEscape})
	pager = next.(*Model)
	if pager.current != modeChat {
		t.Fatalf("second Esc should exit transcript viewer")
	}
}

func TestNotifyHelpersRespectDisableEnv(t *testing.T) {
	t.Setenv("TERMIXGO_NO_BELL", "1")
	if cmd := bell(); cmd != nil {
		t.Fatalf("TERMIXGO_NO_BELL=1 should disable bell command")
	}
}
