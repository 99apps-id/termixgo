package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestCtrlCClearsThenAsksThenQuits(t *testing.T) {
	model := chatModel(t)
	model.composer.SetValue("draft")

	next, cmd := model.Update(key("ctrl+c"))
	cleared := next.(*Model)
	if cleared.composer.Value() != "" || isQuit(cmd) {
		t.Fatalf("the first Ctrl+C should clear the draft, got %q", cleared.composer.Value())
	}
	next, cmd = cleared.Update(key("ctrl+c"))
	asked := next.(*Model)
	if isQuit(cmd) || !strings.Contains(asked.notice, "Ctrl+C again") {
		t.Fatalf("an empty composer should ask before quitting, notice = %q", asked.notice)
	}
	_, cmd = asked.Update(key("ctrl+c"))
	if !isQuit(cmd) {
		t.Fatalf("the confirming Ctrl+C should quit")
	}
}

func TestCtrlCInterruptsARunningTurn(t *testing.T) {
	model := chatModel(t)
	model.running = true
	next, cmd := model.Update(key("ctrl+c"))
	if isQuit(cmd) || !strings.Contains(next.(*Model).notice, "Interrupting") {
		t.Fatalf("Ctrl+C during a run should interrupt, notice = %q", next.(*Model).notice)
	}
}

func TestCtrlXConfirmsWhenWorkWouldBeLost(t *testing.T) {
	idle := chatModel(t)
	if _, cmd := idle.Update(key("ctrl+x")); !isQuit(cmd) {
		t.Fatalf("Ctrl+X on an idle empty composer quits at once")
	}

	busy := chatModel(t)
	busy.composer.SetValue("unsent")
	next, cmd := busy.Update(key("ctrl+x"))
	if isQuit(cmd) {
		t.Fatalf("Ctrl+X with a draft must ask first")
	}
	if _, cmd = next.Update(key("ctrl+x")); !isQuit(cmd) {
		t.Fatalf("the second Ctrl+X should quit")
	}
}
