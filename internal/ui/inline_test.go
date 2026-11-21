package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

func inlineModel(t *testing.T) *Model {
	t.Helper()
	model := chatModel(t)
	model.inline = true
	model.refresh()
	return model
}

// The welcome is final at start, so it goes to the scrollback once.
func TestInlinePrintsSettledBlocksOnce(t *testing.T) {
	model := inlineModel(t)
	if model.printed != len(model.blocks) || len(model.pendingPrint) != 1 {
		t.Fatalf("printed %d of %d, pending %d", model.printed, len(model.blocks), len(model.pendingPrint))
	}
	if cmd := model.takePrints(nil); cmd == nil || len(model.pendingPrint) != 0 {
		t.Fatalf("the queued blocks should become one print command")
	}
	model.refresh()
	if len(model.pendingPrint) != 0 {
		t.Fatalf("a block must not be printed twice")
	}
}

// During a turn the growing tail stays in the live area; each block is
// printed when the next one begins, and the rest when the turn ends.
func TestInlineKeepsTheLiveTailUntilItIsFinal(t *testing.T) {
	model := inlineModel(t)
	model.pendingPrint = nil
	model.blocks = append(model.blocks, block{kind: blockUser, text: "fix the parser"})
	model.running = true
	model.refresh()
	if len(model.pendingPrint) != 0 {
		t.Fatalf("the prompt of the running turn is the live tail")
	}

	model.applyEvent(agent.Event{Kind: agent.EventToolStart, ToolName: "read_file", ToolLabel: "Reading parser.go"})
	model.refresh()
	if len(model.pendingPrint) != 1 || !strings.Contains(stripANSI(model.pendingPrint[0]), "fix the parser") {
		t.Fatalf("the prompt should print once a tool starts: %q", model.pendingPrint)
	}
	if view := stripANSI(model.View()); !strings.Contains(view, "Reading parser.go") || strings.Contains(view, "fix the parser") {
		t.Fatalf("the live area holds only the running tool:\n%s", view)
	}

	model.applyEvent(agent.Event{Kind: agent.EventToolEnd, ToolName: "read_file", ToolLabel: "Read parser.go", ToolOK: true})
	model.applyEvent(agent.Event{Kind: agent.EventText, Text: "The bug is on line 12."})
	model.pendingPrint = nil
	model.refresh()
	if len(model.pendingPrint) != 1 || !strings.Contains(stripANSI(model.pendingPrint[0]), "Read parser.go") {
		t.Fatalf("the finished tool should print: %q", model.pendingPrint)
	}

	model.running = false
	model.pendingPrint = nil
	model.refresh()
	if len(model.pendingPrint) != 1 || !strings.Contains(stripANSI(model.pendingPrint[0]), "line 12") {
		t.Fatalf("the answer should print when the turn ends: %q", model.pendingPrint)
	}
	if view := stripANSI(model.View()); strings.Contains(view, "line 12") {
		t.Fatalf("a printed block must leave the live area:\n%s", view)
	}
}

// A long live tail keeps its newest lines and the frame fits the terminal.
func TestInlineLiveAreaFitsTheTerminal(t *testing.T) {
	model := inlineModel(t)
	model.running = true
	model.blocks = append(model.blocks, block{kind: blockAssistant, text: strings.Repeat("streamed line\n", 200) + "newest"})
	model.refresh()
	view := model.View()
	if lines := strings.Count(view, "\n") + 1; lines > model.height {
		t.Fatalf("the frame has %d lines for a %d-line terminal", lines, model.height)
	}
	if !strings.Contains(stripANSI(view), "newest") {
		t.Fatalf("the newest streamed line must stay visible")
	}
}

// /new resets the transcript; the inline layout prints the fresh one.
func TestInlineReprintsAfterAReset(t *testing.T) {
	model := inlineModel(t)
	model.pendingPrint = nil
	next, _ := model.runSlash("new", "")
	reset := next.(*Model)
	reset.refresh()
	if len(reset.pendingPrint) == 0 {
		t.Fatalf("the new session's opening should be printed")
	}
}

func TestInlineIsTheDefaultLayout(t *testing.T) {
	t.Setenv("TERMIXGO_FULLSCREEN", "")
	if !inlineLayout(config.Config{}) {
		t.Fatalf("inline should be the default")
	}
	if inlineLayout(config.Config{TUILayout: "fullscreen"}) {
		t.Fatalf("fullscreen in the config should win")
	}
	t.Setenv("TERMIXGO_FULLSCREEN", "1")
	if inlineLayout(config.Config{}) {
		t.Fatalf("TERMIXGO_FULLSCREEN should select the alternate screen")
	}
}

// The real program in the inline layout neither enters the alternate screen
// nor captures the mouse, and prints the opening into the output.
func TestRunInlineUsesTheScrollback(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	application := plainApp(t)
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(application, Options{
			Input:  &delayedReader{data: []byte{0x18}, delay: 500 * time.Millisecond},
			Output: &out,
			Layout: "inline",
		})
	}()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
			t.Fatalf("RunWithOptions returned %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("the program did not return")
	}
	written := out.String()
	if strings.Contains(written, "\x1b[?1049h") {
		t.Fatalf("the inline layout must not enter the alternate screen")
	}
	if strings.Contains(written, "\x1b[?1002h") || strings.Contains(written, "\x1b[?1006h") {
		t.Fatalf("the inline layout must not capture the mouse")
	}
	if !strings.Contains(stripANSI(written), "folder") {
		t.Fatalf("the opening block should be printed into the scrollback:\n%q", stripANSI(written))
	}
}

// delayedReader sends its bytes after a pause, so the program has started and
// printed before the quit key arrives, then blocks.
type delayedReader struct {
	data  []byte
	delay time.Duration
	read  bool
}

func (r *delayedReader) Read(p []byte) (int, error) {
	if r.read {
		time.Sleep(time.Hour)
		return 0, nil
	}
	time.Sleep(r.delay)
	r.read = true
	return copy(p, r.data), nil
}

type lockedBuffer struct {
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) { return b.buffer.Write(p) }
func (b *lockedBuffer) String() string              { return b.buffer.String() }
