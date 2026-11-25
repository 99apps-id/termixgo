package ui

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// Inline layout
//
// In the inline layout the program does not take over the screen. A block is
// printed once into the terminal's scrollback as soon as it is final, and the
// redrawn area is only what is still changing: the live tail of the turn in
// flight, the status line and the composer. That is how Claude Code works,
// and it hands scrolling, selection and copy back to the terminal.

// inlineLayout reports whether the configuration asks for the inline layout.
// It is the default; "fullscreen" or TERMIXGO_FULLSCREEN=1 selects the
// alternate screen.
func inlineLayout(cfg config.Config) bool {
	if value := strings.TrimSpace(os.Getenv("TERMIXGO_FULLSCREEN")); value != "" && value != "0" {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(cfg.TUILayout), "fullscreen")
}

// settledCount is how many leading blocks are final. With no turn running
// every block is; during a turn the last block is still growing, a running
// tool or an open thinking block still changes, and order is kept by stopping
// at the first block that is not final.
func (m *Model) settledCount() int {
	total := len(m.blocks)
	if !m.running {
		return total
	}
	for index := m.printed; index < total; index++ {
		item := m.blocks[index]
		if index == total-1 || item.running || (item.kind == blockThinking && !item.finalized) {
			return index
		}
		if !m.showDetails && isCompactGroupableTool(item) && allTrailingGroupableTools(m.blocks[index+1:]) {
			return index
		}
	}
	return total
}

func allTrailingGroupableTools(tail []block) bool {
	if len(tail) == 0 {
		return false
	}
	for _, item := range tail {
		if item.kind != blockTool {
			return false
		}
		if item.running {
			// A running read/search tool may finish and join this group; a
			// running mutating tool will not, so let earlier reads settle.
			switch item.toolName {
			case "read_file", "read_document", "read_image",
				"list_directory",
				"grep", "glob", "code_outline", "symbol_search", "symbol_references", "search_memory",
				"web_search", "web_fetch", "lookup":
				continue
			default:
				return false
			}
		}
		if !isCompactGroupableTool(item) {
			return false
		}
	}
	return true
}

// flushSettled queues every newly final block for printing into the
// scrollback. A transcript that was reset (/new, /clear) starts over.
func (m *Model) flushSettled() {
	// Nothing is printed while the transcript pager holds the alternate
	// screen: a print there is dropped, so it waits until the pager closes.
	if !m.inline || m.current == modeTranscript {
		return
	}
	if m.printed > len(m.blocks) {
		m.printed = 0
	}
	end := m.settledCount()
	if end <= m.printed {
		m.flushStreamingAnswer()
		return
	}
	defer m.flushStreamingAnswer()
	hide := m.app != nil && !m.app.Config().ShowReasoning
	slice := unprintedBlocks(m.blocks[m.printed:end])
	if m.running {
		filtered := make([]block, 0, len(slice))
		for _, item := range slice {
			// While a turn is running, the live plan stays pinned above the
			// status bar rather than scrolling away mid-turn.
			if item.kind != blockPlan {
				filtered = append(filtered, item)
			}
		}
		slice = filtered
	}
	m.printed = end
	if len(slice) == 0 {
		return
	}
	rendered := transcript(hideReasoning(slice, hide), m.styles, m.renderWidth(), m.showDetails)
	if strings.TrimSpace(rendered) != "" {
		m.pendingPrint = append(m.pendingPrint, rendered)
	}
}

// imagePasteKey names the key that pastes an image. Windows Terminal keeps
// Ctrl+V (and Ctrl+Shift+V) for its own text paste and sends nothing when the
// clipboard holds only an image, so on Windows the key that reaches the
// program is Alt+V.
func imagePasteKey() string {
	if runtime.GOOS == "windows" {
		return "Alt+V"
	}
	return "Ctrl+V"
}

// resizeSettleDelay is how long the width must stay still before the
// transcript is printed again at the new width.
const resizeSettleDelay = 150 * time.Millisecond

// reprintBlockCap bounds a reprint, so a resize in a very long session does
// not stall on printing every block again.
const reprintBlockCap = 400

type resizeSettledMsg struct{ seq int }

func resizeSettleCmd(seq int) tea.Cmd {
	return tea.Tick(resizeSettleDelay, func(time.Time) tea.Msg { return resizeSettledMsg{seq: seq} })
}

// reprintAll clears the screen and the scrollback and prints the transcript
// again at the current width. A resize leaves the printed blocks wrapped at
// the old width, and a narrower terminal turns their full-width lines into
// broken ones; printing them again is the only way to tidy what the terminal
// already holds.
func (m *Model) reprintAll() tea.Cmd {
	start := 0
	if len(m.blocks) > reprintBlockCap {
		start = len(m.blocks) - reprintBlockCap
	}
	m.printed = start
	m.pendingPrint = nil
	// A streamed answer is printed again whole: the scrollback that held its
	// first paragraphs is the one being cleared.
	for index := start; index < len(m.blocks); index++ {
		m.blocks[index].streamed = 0
	}
	m.flushSettled()
	text := strings.Join(m.pendingPrint, "\n\n")
	m.pendingPrint = nil
	if start > 0 {
		text = m.styles.Dim.Render(fmt.Sprintf("  ... %d earlier blocks, Ctrl+O shows the whole transcript", start)) + "\n\n" + text
	}
	return tea.Sequence(reprintCmds(text, m.realTerminal)...)
}

// eraseScrollback is ED 3, which clears the terminal's scrollback.
const eraseScrollback = "\x1b[3J"

// reprintCmds clears the screen, then prints the transcript. The scrollback
// erase rides at the head of the printed text, so the renderer writes it under
// its own lock, after its screen clear and before the reprinted lines. Writing
// it straight to stdout from a command goroutine raced the renderer's frame
// writes, and a frame flushed after the erase was left in the scrollback.
func reprintCmds(text string, eraseHistory bool) []tea.Cmd {
	cmds := []tea.Cmd{tea.ClearScreen}
	printed := ""
	if strings.TrimSpace(text) != "" {
		printed = text + "\n"
	}
	if eraseHistory {
		printed = eraseScrollback + printed
	}
	if printed != "" {
		cmds = append(cmds, tea.Println(printed))
	}
	return cmds
}

// takePrints wraps cmd so the queued blocks are printed above the live area
// before anything else the update asked for.
func (m *Model) takePrints(cmd tea.Cmd) tea.Cmd {
	if !m.inline || len(m.pendingPrint) == 0 {
		return cmd
	}
	text := strings.Join(m.pendingPrint, "\n\n") + "\n"
	m.pendingPrint = nil
	if cmd == nil {
		return tea.Println(text)
	}
	return tea.Sequence(tea.Println(text), cmd)
}

// inlineView renders the live area: the blocks not yet printed, the pinned live
// plan (if any), the status line and the composer or the open overlay.
func (m *Model) inlineView() string {
	overlay := ""
	switch {
	case m.pendingApproval != nil:
		overlay = m.viewApproval()
	case m.pendingAsk != nil:
		overlay = m.viewAsk()
	case m.pendingPlan != nil:
		overlay = m.viewPlanReview()
	case m.historySearchActive:
		overlay = m.viewHistorySearch()
	case m.slashMenuOpen():
		overlay = m.viewSlashMenu()
	case len(m.mentionMatches) > 0:
		overlay = m.viewMentionMenu()
	}
	var bottom []string
	if activity := m.activityLine(); activity != "" {
		bottom = append(bottom, activity)
	}
	if m.running && m.app != nil {
		// A plan with every item done is history, not status: the final list is
		// already in the transcript. The pinned plan is windowed: an unbounded
		// plan makes the bottom block taller than the terminal, and the frame
		// fit then drops the top instead of the plan.
		if todos := m.app.Todos().Items(); len(todos) > 0 && agent.PruneFinished(todos) != nil {
			status := m.viewStatus()
			composer := m.viewComposer() + "\n" + m.viewHints()
			reserved := lipgloss.Height(status) + lipgloss.Height(composer) + 2
			if overlay != "" {
				reserved += lipgloss.Height(overlay)
			} else if strip := m.viewQuota(); strip != "" {
				reserved += lipgloss.Height(strip)
			}
			maxRows := max(1, m.height-reserved)
			shown := renderPlanBlock(block{kind: blockPlan, plan: todos}, m.styles, m.renderWidth(), maxRows)
			if planView := strings.TrimSpace(shown); planView != "" {
				bottom = append(bottom, planView)
			}
		}
	}
	bottom = append(bottom, m.viewStatus())
	if overlay != "" {
		bottom = append(bottom, overlay)
	} else {
		if strip := m.viewQuota(); strip != "" {
			bottom = append(bottom, strip)
		}
		bottom = append(bottom, m.viewComposer(), m.viewHints())
	}
	bottomText := lipgloss.JoinVertical(lipgloss.Left, bottom...)

	var sections []string
	if m.printed < len(m.blocks) {
		hide := m.app != nil && !m.app.Config().ShowReasoning
		liveBlocks := liveWithoutRunningThinking(unprintedBlocks(m.blocks[m.printed:]), m.showDetails)
		if m.running {
			filtered := make([]block, 0, len(liveBlocks))
			for _, item := range liveBlocks {
				if item.kind != blockPlan {
					filtered = append(filtered, item)
				}
			}
			liveBlocks = filtered
		}
		live := transcript(hideReasoning(liveBlocks, hide), m.styles, m.renderWidth(), m.showDetails)
		if strings.TrimSpace(live) != "" {
			lines := strings.Split(live, "\n")
			budget := m.height - lipgloss.Height(bottomText) - 1
			if budget < 1 {
				budget = 1
			}
			if len(lines) > budget {
				lines = lines[len(lines)-budget:]
			}
			sections = append(sections, strings.Join(lines, "\n"), "")
		}
	}
	sections = append(sections, bottomText)
	return strings.Join(sections, "\n")
}
