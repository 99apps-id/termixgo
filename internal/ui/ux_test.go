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
