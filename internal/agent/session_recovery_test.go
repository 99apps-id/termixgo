package agent

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

func TestInterruptedTurnIsClosedOnLoad(t *testing.T) {
	dir := t.TempDir()
	session := NewSession(t.TempDir(), "test-model")
	session.SetStoreDir(dir)
	session.AddUser("change the parser")
	session.AddAssistant("", "", []provider.ToolCall{
		{ID: "call-1", Name: "edit", Arguments: `{}`},
		{ID: "call-2", Name: "run_command", Arguments: `{}`},
	}, nil)
	session.AddToolResult("call-1", "edit", "edited")
	session.BeginTurn()
	if err := session.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadSessionIn(dir, session.ID())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	notice := loaded.RecoveryNotice()
	if !strings.Contains(notice, "1 tool call") {
		t.Fatalf("notice = %q", notice)
	}
	if loaded.InFlight() {
		t.Fatal("the marker must be cleared by recovery")
	}

	messages := loaded.Messages()
	last := messages[len(messages)-1]
	if last.Role != provider.RoleTool || last.ToolID != "call-2" || last.Name != "run_command" {
		t.Fatalf("last message = %+v", last)
	}
	if !strings.Contains(last.Content, "unknown") {
		t.Fatalf("the synthetic result should say the effect is unknown: %q", last.Content)
	}
	// The call that did finish keeps exactly one result.
	count := 0
	for _, message := range messages {
		if message.Role == provider.RoleTool && message.ToolID == "call-1" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("call-1 has %d results", count)
	}
}

func TestInterruptedTurnWithoutCallsIsCleared(t *testing.T) {
	dir := t.TempDir()
	session := NewSession(t.TempDir(), "test-model")
	session.SetStoreDir(dir)
	session.AddUser("answer me")
	session.BeginTurn()
	if err := session.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadSessionIn(dir, session.ID())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.RecoveryNotice() == "" {
		t.Fatal("an interrupted turn should be reported even with no call to close")
	}
	if len(loaded.Messages()) != 1 {
		t.Fatalf("messages = %d, want the prompt only", len(loaded.Messages()))
	}
}

func TestCompletedTurnIsNotReportedAsInterrupted(t *testing.T) {
	dir := t.TempDir()
	session := NewSession(t.TempDir(), "test-model")
	session.SetStoreDir(dir)
	session.AddUser("hello")
	session.AddAssistant("hi", "", nil, nil)
	session.BeginTurn()
	session.EndTurn()
	if err := session.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadSessionIn(dir, session.ID())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if notice := loaded.RecoveryNotice(); notice != "" {
		t.Fatalf("a finished turn was reported: %q", notice)
	}
}

func TestRecoveryHappensOnce(t *testing.T) {
	dir := t.TempDir()
	session := NewSession(t.TempDir(), "test-model")
	session.SetStoreDir(dir)
	session.AddUser("go")
	session.AddAssistant("", "", []provider.ToolCall{{ID: "call-1", Name: "edit"}}, nil)
	session.BeginTurn()
	if err := session.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	first, err := LoadSessionIn(dir, session.ID())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if first.RecoveryNotice() == "" {
		t.Fatal("the first load should report the interruption")
	}
	if err := first.Save(); err != nil {
		t.Fatalf("save recovered: %v", err)
	}

	second, err := LoadSessionIn(dir, session.ID())
	if err != nil {
		t.Fatalf("load again: %v", err)
	}
	if notice := second.RecoveryNotice(); notice != "" {
		t.Fatalf("the closure was not persisted: %q", notice)
	}
	if len(second.Messages()) != len(first.Messages()) {
		t.Fatalf("messages grew on the second load: %d -> %d", len(first.Messages()), len(second.Messages()))
	}
}
