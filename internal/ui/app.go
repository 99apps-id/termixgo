package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/app"
	"github.com/99apps-id/termixgo/internal/command"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/version"
)

// mode is what the program is showing.
type mode int

const (
	modeChat mode = iota
	modeHelp
	modePicker
	modeSessions
	modeSetup
)

// msg types.
type (
	eventMsg           agent.Event
	tickMsg            time.Time
	runDoneMsg         struct{ err error }
	approvalRequestMsg struct {
		request agent.ApprovalRequest
		reply   chan agent.Decision
	}
	askRequestMsg struct {
		question string
		options  []string
		reply    chan string
	}
	telegramPairedMsg struct{ paired bool }
)

// Model is the Bubble Tea program state.
type Model struct {
	app    *app.App
	styles Styles
	limits Limits

	program *tea.Program

	width  int
	height int
	ready  bool

	viewport viewport.Model
	composer textarea.Model
	input    textinput.Model
	spin     spinner.Model

	blocks []block

	slashMatches []SlashCommand
	slashCursor  int

	// mentionMatches holds the @file candidates for the token being typed.
	// Tab accepts the highlighted one; Enter leaves the text alone.
	mentionMatches []string
	mentionCursor  int
	mentionAt      int

	// custom holds user-defined slash commands from
	// .termixgo/commands/*.md. It refreshes on a TTL so a new file
	// appears in the menu without restarting the program.
	custom   []command.Command
	customAt time.Time

	current mode
	picker  picker
	setup   setupState

	notice string

	running    bool
	runStarted time.Time

	// showDetails controls whether thinking, reasoning and tool process
	// blocks are expanded or collapsed. Ctrl+O toggles the value.
	showDetails bool

	// queue holds operator inputs typed while a turn is running. Enter
	// queues the composer text instead of refusing it, and each runDone
	// starts the next queued input, so a steer message is never lost.
	queue []string

	pendingApproval *agent.ApprovalRequest
	approvalReply   chan agent.Decision
	// approvalCursor is the highlighted approval option. Arrow keys move it
	// and Enter confirms it, so the operator is not limited to letter keys.
	approvalCursor int

	// lastPaint is when the transcript was last pushed into the viewport.
	// Streaming deltas update the blocks at once but repaint at most this
	// often, so a fast model cannot flood the terminal with full frames.
	lastPaint  time.Time
	pendingAsk *askRequestMsg
}

// Limits bundles size thresholds used by the layout and the setup wizard.
type Limits struct {
	MinWidth  int
	MinHeight int
}

// DefaultLimits returns the layout thresholds.
func DefaultLimits() Limits { return Limits{MinWidth: 50, MinHeight: 16} }

// Options configures how the program starts.
type Options struct {
	// StartSetup opens the onboarding wizard immediately.
	StartSetup bool

	// Input and Output override the streams the program reads and draws on,
	// defaulting to the process's own. Supplying them lets a caller drive the
	// UI over its own pipes, which is what makes the entry point testable
	// without a terminal.
	Input  io.Reader
	Output io.Writer

	// NoAlternateScreen keeps the transcript in the terminal's scrollback
	// instead of taking over the screen. It suits an embedded or logged run,
	// where losing the scrollback is worse than losing the fixed layout.
	NoAlternateScreen bool
}

// composerHeight is the composer rows. Five fits a two-line prompt plus a
// line of @file context without stealing the transcript, which owns the
// rest of the window.
// composerHeight is the composer rows. Five fits a two-line prompt plus a
// hint without the box jumping as the text grows.
const composerHeight = 5

// composerPromptWidth is the columns the composer marker occupies. The
// textarea is told this width so it reserves the room itself; adding the
// marker after layout instead made every wrapped line two columns too long.
const composerPromptWidth = 2

// composerPrompt marks the first line and indents the rest, so a wrapped
// message stays aligned under the marker instead of repeating it on every
// visual line.
func composerPrompt(line int) string {
	if line == 0 {
		return "> "
	}
	return "  "
}

// New builds the program model for an app.
func New(application *app.App) *Model { return NewWithOptions(application, Options{}) }

// NewWithOptions builds the program model with explicit start options.
func NewWithOptions(application *app.App, options Options) *Model {
	styles := NewStyles(DefaultPalette())

	composer := textarea.New()
	composer.Placeholder = "Ask Termixgo to change something, or type /help"
	composer.ShowLineNumbers = false
	composer.CharLimit = 32000
	composer.SetHeight(composerHeight)
	// The marker is part of the textarea rather than painted over it, so it is
	// counted when the text wraps.
	composer.SetPromptFunc(composerPromptWidth, composerPrompt)
	// Enter submits; ctrl+j and alt+enter insert a line break.
	composer.KeyMap.InsertNewline.SetKeys("ctrl+j", "alt+enter")

	field := textinput.New()
	field.CharLimit = 4096

	pickerList := newPicker()

	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = lipgloss.NewStyle().Foreground(styles.Palette.Accent2)

	model := &Model{
		app:         application,
		styles:      styles,
		limits:      DefaultLimits(),
		viewport:    viewport.New(80, 20),
		composer:    composer,
		input:       field,
		spin:        spin,
		picker:      pickerList,
		current:     modeChat,
		showDetails: true,
	}
	application.SetInteractor(model)
	_ = model.reloadCustomCommands()
	model.welcome()
	if options.StartSetup || application.NeedsSetup() {
		model.startSetup()
	} else {
		model.composer.Focus()
	}
	return model
}

// RunWithOptions starts the program with explicit options.
func RunWithOptions(application *app.App, options Options) error {
	model := NewWithOptions(application, options)

	programOptions := []tea.ProgramOption{tea.WithMouseCellMotion()}
	if !options.NoAlternateScreen {
		programOptions = append(programOptions, tea.WithAltScreen())
	}
	if options.Input != nil {
		programOptions = append(programOptions, tea.WithInput(options.Input))
	}
	if options.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(options.Output))
	}

	program := tea.NewProgram(model, programOptions...)
	model.program = program
	_, err := program.Run()
	return err
}

// welcome appends the opening screen block.
func (m *Model) welcome() {
	trust := m.styles.NoTrust.Render("untrusted")
	if m.app.Trusted() {
		trust = m.styles.Trust.Render("trusted")
	}
	modelLabel := m.app.ModelLabel()
	if strings.TrimSpace(modelLabel) == "" {
		modelLabel = m.styles.NoTrust.Render("no model yet")
	}
	info := []string{
		m.styles.Title.Render(fmt.Sprintf("%s %s", version.Name, version.Version)),
		"",
		m.styles.StatusKey.Render("folder  ") + m.styles.StatusValue.Render(m.app.Workspace()) + "  " + trust,
		m.styles.StatusKey.Render("model   ") + modelLabel,
		"",
		m.styles.Subtitle.Render("Type a request and press Enter. /help lists commands, /setup finishes onboarding."),
	}
	if !m.app.Trusted() {
		info = append(info, m.styles.Hint.Render("This folder is untrusted: writes and commands will ask first. Run /trust on to change that."))
	}
	m.blocks = append(m.blocks, block{kind: blockWelcome, text: strings.Join(info, "\n")})
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spin.Tick, tea.SetWindowTitle(m.windowTitle()))
}

// windowTitle is the terminal tab title. It mirrors the run state so the
// operator sees working, approval or idle without looking at the pane, the
// way Codex CLI keeps its tab informative.
func (m *Model) windowTitle() string {
	base := "Termixgo"
	if workspace := strings.TrimSpace(m.app.Workspace()); workspace != "" {
		base += " - " + filepath.Base(workspace)
	}
	switch {
	case m.pendingApproval != nil:
		return base + " - approval needed"
	case m.running:
		elapsed := time.Since(m.runStarted).Round(time.Second)
		return fmt.Sprintf("%s - working %s", base, elapsed)
	default:
		return base
	}
}

// Update implements tea.Model.
func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := message.(type) {
	case tea.WindowSizeMsg:
		m.width = typed.Width
		m.height = typed.Height
		m.ready = true
		m.layout()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(typed)

	case eventMsg:
		event := agent.Event(typed)
		m.applyEvent(event)
		// Text deltas arrive in bursts during a fast stream, and every
		// repaint rebuilds the whole transcript. Painting each one flickers
		// the terminal and starves it; structural events still paint at
		// once so tool boundaries never lag behind the text.
		m.maybeRefresh(event.Kind == agent.EventText || event.Kind == agent.EventThinking)
		return m, waitForEvent(m.app.Events())

	case tickMsg:
		if m.running {
			m.refresh()
			return m, tea.Batch(m.spin.Tick, tick(), tea.SetWindowTitle(m.windowTitle()))
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(typed)
		return m, cmd

	case runDoneMsg:
		m.running = false
		// A turn that ended while an approval or question was pending leaves
		// those dialogs orphaned. Clear them and send the safe default on
		// the reply channel so the goroutine that created it is never stuck.
		if m.pendingApproval != nil {
			if m.approvalReply != nil {
				select {
				case m.approvalReply <- agent.DecisionDeny:
				default:
				}
			}
			m.pendingApproval = nil
			m.approvalReply = nil
		}
		if m.pendingAsk != nil {
			if m.pendingAsk.reply != nil {
				select {
				case m.pendingAsk.reply <- "":
				default:
				}
			}
			m.pendingAsk = nil
		}
		if typed.err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: typed.err.Error()})
		}
		// Anything the finished turn never took becomes the next input, in the
		// order it was typed, so a steer that arrived too late still runs.
		m.queue = append(m.queue, m.app.TakeSteer()...)
		if len(m.queue) > 0 {
			next := m.queue[0]
			m.queue = m.queue[1:]
			m.notice = ""
			m.refresh()
			return m.submit(next)
		}
		m.refresh()
		return m, tea.SetWindowTitle(m.windowTitle())

	case approvalRequestMsg:
		request := typed.request
		m.pendingApproval = &request
		m.approvalReply = typed.reply
		m.approvalCursor = 0
		m.refresh()
		return m, tea.SetWindowTitle(m.windowTitle())

	case askRequestMsg:
		m.pendingAsk = &typed
		m.input.Placeholder = "Type an answer"
		m.input.Focus()
		m.refresh()
		return m, textarea.Blink

	case telegramPairedMsg:
		if typed.paired {
			m.notice = "Telegram paired."
		}
		m.refresh()
		return m, nil

	case tea.MouseMsg:
		// The wheel scrolls the transcript. Keys stay with the composer:
		// forwarding them to the viewport would scroll while typing.
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(message)
		return m, cmd
	}

	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(message)
	m.updateSlashMatches()
	m.refresh()
	return m, cmd
}

// handleKey routes keys by what is currently on screen.
func (m *Model) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global quit.
	if key.String() == "ctrl+c" {
		return m, tea.Quit
	}

	if m.pendingApproval != nil {
		return m.handleApprovalKey(key)
	}
	if m.pendingAsk != nil {
		return m.handleAskKey(key)
	}

	switch m.current {
	case modeHelp:
		m.current = modeChat
		m.refresh()
		return m, nil
	case modeSetup:
		return m.handleSetupKey(key)
	case modePicker, modeSessions:
		return m.handlePickerKey(key)
	}

	switch key.String() {
	case "ctrl+o":
		m.showDetails = !m.showDetails
		if m.showDetails {
			m.notice = "Details visible (Ctrl+O to hide)"
		} else {
			m.notice = "Details hidden (Ctrl+O to show)"
		}
		m.refresh()
		return m, nil
	case "esc":
		if m.running {
			m.app.Stop()
			if count := m.queuedCount(); count > 0 {
				m.notice = fmt.Sprintf("Stopping... (%d queued)", count)
			} else {
				m.notice = "Stopping..."
			}
			return m, nil
		}
		if len(m.slashMatches) > 0 || len(m.mentionMatches) > 0 {
			// First Esc steps back to typing and keeps the text; a second
			// Esc clears the composer.
			m.slashMatches = nil
			m.mentionMatches = nil
			m.refresh()
			return m, nil
		}
		m.composer.SetValue("")
		m.slashMatches = nil
		m.mentionMatches = nil
		m.refresh()
		return m, nil
	case "enter":
		raw := m.composer.Value()
		value := strings.TrimSpace(raw)
		if value == "" {
			return m, nil
		}
		// The last row is the way back: highlighting it and pressing Enter
		// or Tab closes the menu and keeps the typed text, for operators
		// who navigated past the command they wanted.
		if len(m.slashMatches) > 0 && m.slashCursor == len(m.slashMatches) {
			m.slashMatches = nil
			m.refresh()
			return m, nil
		}
		// Accept the highlighted slash completion: typing "/stat" and
		// pressing Enter must run /status, not report an unknown command.
		// Text with arguments is already explicit and passes through.
		// The menu cache can be stale on the very first Enter, so fall back
		// to a fresh match instead of trusting it blindly.
		if !strings.Contains(value, " ") && strings.HasPrefix(value, "/") {
			matches := m.slashMatches
			if len(matches) == 0 {
				matches = MatchSlash(value)
			}
			if len(matches) > 0 {
				cursor := m.slashCursor
				if cursor < 0 || cursor >= len(matches) {
					cursor = 0
				}
				if matches[cursor].Trigger != value {
					value = matches[cursor].Trigger
				}
			}
		}
		// A command followed by a space is the operator asking for its
		// arguments. The prefix menu has nothing to complete at that point, so
		// without this the argument list is only reachable by memory.
		if strings.HasSuffix(raw, " ") && strings.Count(value, " ") == 0 && strings.HasPrefix(value, "/") {
			name := strings.TrimPrefix(value, "/")
			if _, ok := FindSlash(name); ok && len(SlashOptions(name)) > 0 {
				m.composer.SetValue(slashTrigger(name) + " ")
				m.slashMatches = nil
				m.mentionMatches = nil
				return m.openSlashArgs(name)
			}
		}
		if slashName, _, ok := ParseSlash(value); ok {
			// Only free text can steer a running turn. A slash command is a
			// local control, so sending it to the model as prose is wrong: the
			// operator typed /cost and the agent read the literal text "/cost".
			// A command that must act on the live turn runs at once, a read-only
			// one runs at once because it cannot disturb the turn, and anything
			// that starts work behind it is queued for the drain.
			if m.running {
				if liveSlashCommand(slashName) {
					m.composer.SetValue("")
					m.slashMatches = nil
					m.mentionMatches = nil
					return m.submit(value)
				}
				m.composer.SetValue("")
				m.slashMatches = nil
				m.mentionMatches = nil
				return m.enqueue(value)
			}
		}
		if m.running {
			return m.enqueue(value)
		}
		m.composer.SetValue("")
		m.slashMatches = nil
		m.mentionMatches = nil
		return m.submit(value)
	case "up":
		if len(m.mentionMatches) > 0 {
			m.mentionCursor = (m.mentionCursor - 1 + len(m.mentionMatches)) % len(m.mentionMatches)
			return m, nil
		}
		if len(m.slashMatches) > 0 {
			// The range holds one extra row: the back option past the end.
			m.slashCursor = (m.slashCursor - 1 + len(m.slashMatches) + 1) % (len(m.slashMatches) + 1)
			return m, nil
		}
	case "down":
		if len(m.mentionMatches) > 0 {
			m.mentionCursor = (m.mentionCursor + 1) % len(m.mentionMatches)
			return m, nil
		}
		if len(m.slashMatches) > 0 {
			m.slashCursor = (m.slashCursor + 1) % (len(m.slashMatches) + 1)
			return m, nil
		}
	case "tab":
		if len(m.mentionMatches) > 0 {
			cursor := m.mentionCursor
			if cursor < 0 || cursor >= len(m.mentionMatches) {
				cursor = 0
			}
			m.composer.SetValue(applyMentionCompletion(m.composer.Value(), m.mentionMatches[cursor], m.mentionAt))
			m.mentionMatches = nil
			m.updateSlashMatches()
			return m, nil
		}
		if len(m.slashMatches) > 0 {
			if m.slashCursor == len(m.slashMatches) {
				// Tab on the back row steps back to typing as Enter does.
				m.slashMatches = nil
				return m, nil
			}
			trigger := strings.TrimPrefix(m.slashMatches[m.slashCursor].Trigger, "/")
			m.composer.SetValue(m.slashMatches[m.slashCursor].Trigger + " ")
			m.slashMatches = nil
			if len(SlashOptions(trigger)) == 0 {
				return m, nil
			}
			// The command takes an argument from a known set, so completing it
			// to bare text would leave the operator to remember the values.
			// Opening the argument menu is what makes the menu usable rather
			// than read-only.
			return m.openSlashArgs(trigger)
		}
	case "pgup":
		m.viewport.HalfViewUp()
		m.refresh()
		return m, nil
	case "pgdown":
		m.viewport.HalfViewDown()
		m.refresh()
		return m, nil
	case "home":
		m.viewport.GotoTop()
		m.refresh()
		return m, nil
	case "end":
		m.viewport.GotoBottom()
		m.refresh()
		return m, nil
	}

	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(key)
	m.updateSlashMatches()
	m.refresh()
	return m, cmd
}

// approvalOptions is the arrow-navigable list in the approval dialog, in
// display order. The index doubles as the cursor value.
var approvalOptions = []struct {
	key      string
	label    string
	decision agent.Decision
}{
	{"y", "allow once", agent.DecisionAllowOnce},
	{"s", "allow session", agent.DecisionAllowSession},
	{"a", "always", agent.DecisionAllowAlways},
	{"n", "deny", agent.DecisionDeny},
}

func (m *Model) handleApprovalKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "up", "ctrl+p":
		m.approvalCursor = (m.approvalCursor - 1 + len(approvalOptions)) % len(approvalOptions)
		m.refresh()
		return m, nil
	case "down", "ctrl+n":
		m.approvalCursor = (m.approvalCursor + 1) % len(approvalOptions)
		m.refresh()
		return m, nil
	case "enter":
		cursor := m.approvalCursor
		if cursor < 0 || cursor >= len(approvalOptions) {
			cursor = 0
		}
		return m.answerApproval(approvalOptions[cursor].decision)
	}
	decision := agent.DecisionDeny
	answered := true
	switch strings.ToLower(key.String()) {
	case "y":
		decision = agent.DecisionAllowOnce
	case "s":
		decision = agent.DecisionAllowSession
	case "a":
		decision = agent.DecisionAllowAlways
	case "n", "esc":
		decision = agent.DecisionDeny
	default:
		answered = false
	}
	if !answered {
		return m, nil
	}
	return m.answerApproval(decision)
}

// answerApproval delivers one approval decision to the waiting turn and
// records it in the transcript.
func (m *Model) answerApproval(decision agent.Decision) (tea.Model, tea.Cmd) {
	reply := m.approvalReply
	tool := ""
	if m.pendingApproval != nil {
		tool = m.pendingApproval.Tool
	}
	if decision == agent.DecisionAllowAlways {
		m.app.AllowTool(tool)
	}
	if reply != nil {
		reply <- decision
	}
	m.blocks = append(m.blocks, block{
		kind: blockNotice,
		text: fmt.Sprintf("Approval %s for %s", decisionWord(decision), tool),
	})
	m.pendingApproval = nil
	m.approvalReply = nil
	m.refresh()
	return m, tea.SetWindowTitle(m.windowTitle())
}

func decisionWord(decision agent.Decision) string {
	switch decision {
	case agent.DecisionAllowOnce:
		return "granted once"
	case agent.DecisionAllowSession:
		return "granted for this session"
	case agent.DecisionAllowAlways:
		return "always allowed"
	default:
		return "denied"
	}
}

func (m *Model) handleAskKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" {
		m.pendingAsk.reply <- "(no answer)"
		m.pendingAsk = nil
		m.input.SetValue("")
		m.input.Blur()
		m.refresh()
		return m, nil
	}
	if key.String() == "enter" {
		answer := strings.TrimSpace(m.input.Value())
		if answer == "" {
			return m, nil
		}
		m.pendingAsk.reply <- answer
		m.blocks = append(m.blocks, block{kind: blockUser, text: answer})
		m.pendingAsk = nil
		m.input.SetValue("")
		m.input.Blur()
		m.refresh()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// maxQueuedInputs bounds the steer queue so a held Enter key cannot grow
// memory without limit on a small machine.
const maxQueuedInputs = 5

// enqueue holds one input for after the running turn. The composer is
// cleared so the operator can keep typing the next steer on top of it.
//
// When a turn is genuinely in flight the message is handed to it instead, so
// the agent can change course at its next step rather than only after it
// finishes. A message the live turn never reached stays in the app queue and
// is drained by runDoneMsg, so nothing is lost either way.
func (m *Model) enqueue(value string) (tea.Model, tea.Cmd) {
	if m.queuedCount() >= maxQueuedInputs {
		m.notice = fmt.Sprintf("Queue is full (%d). Wait for the current turn.", maxQueuedInputs)
		m.refresh()
		return m, nil
	}
	if shouldSteer(value, m.running && m.app.Running()) {
		m.app.Steer(value)
	} else {
		m.queue = append(m.queue, value)
	}
	m.composer.SetValue("")
	m.slashMatches = nil
	m.mentionMatches = nil
	if len(m.queue) > 0 && m.queue[len(m.queue)-1] == value {
		m.notice = fmt.Sprintf("Queued (%d). It runs when this turn ends.", m.queuedCount())
	} else {
		m.notice = fmt.Sprintf("Queued (%d). The agent picks it up at its next step.", m.queuedCount())
	}
	m.refresh()
	return m, nil
}

// shouldSteer reports whether an input may be handed to the turn in flight.
//
// Only free text can steer a run: it is a course correction the model can act
// on. A slash command is a local control, and handing it to the model made the
// agent read the literal text "/cost" while the operator never saw the answer,
// so a command is always queued and runs as a command when the turn ends.
func shouldSteer(value string, turnRunning bool) bool {
	if _, _, ok := ParseSlash(value); ok {
		return false
	}
	return turnRunning
}

// queuedCount is everything still waiting: what the UI holds plus what the
// live turn has been handed but not yet taken.
func (m *Model) queuedCount() int { return len(m.queue) + m.app.SteerCount() }

// liveSlashCommands are the commands that may run while a turn is in flight.
//
// Two groups belong here. The first is the controls that act on the live turn,
// where queueing would defer them past the moment they are needed: /stop cancels
// the run and /exit quits. The second is the informational and configuration
// commands, which read app state or change what the NEXT turn does, so running
// one cannot disturb the turn in flight.
//
// Everything else, such as /new or a command that starts its own turn, is queued
// and runs when the turn ends. A command name that is not listed here is never
// sent to the model as prose.
var liveSlashCommands = map[string]bool{
	"stop": true, "exit": true, "quit": true,
	"help": true, "?": true, "status": true, "cost": true, "plan": true,
	"tools": true, "harness": true, "trust": true, "approval": true,
	"mcp": true, "skills": true, "memory": true, "ps": true,
}

// liveSlashCommand reports whether a command may run during a turn.
func liveSlashCommand(name string) bool { return liveSlashCommands[name] }

// submit dispatches either a slash command or an agent turn.
func (m *Model) submit(value string) (tea.Model, tea.Cmd) {
	if name, args, ok := ParseSlash(value); ok {
		return m.runSlash(name, args)
	}
	return m.startRun(value)
}

// startRun begins an agent turn and starts pumping events. @file mentions
// stay as typed in the transcript while the turn itself receives the file
// contents, so the operator reads what they wrote and the model reads more.
func (m *Model) startRun(input string) (tea.Model, tea.Cmd) {
	m.blocks = append(m.blocks, block{kind: blockUser, text: input})
	m.running = true
	m.runStarted = time.Now()
	m.refresh()

	application := m.app
	prompt := ExpandMentions(application.Workspace(), input)
	go func() {
		err := application.RunTurn(context.Background(), prompt)
		if m.program != nil {
			m.program.Send(runDoneMsg{err: err})
		}
	}()
	return m, tea.Batch(m.spin.Tick, tick(), waitForEvent(application.Events()), tea.SetWindowTitle(m.windowTitle()))
}

// waitForEvent reads one agent event off the channel.
func waitForEvent(events <-chan agent.Event) tea.Cmd {
	return func() tea.Msg { return eventMsg(<-events) }
}

// tick drives the running indicator.
func tick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// applyEvent folds one agent event into the transcript.
func (m *Model) applyEvent(event agent.Event) {
	switch event.Kind {
	case agent.EventTurnStart:
		// Nothing to show yet; the first chunk creates its block.
	case agent.EventThinking:
		if last := m.lastBlock(); last != nil && last.kind == blockThinking && last.running {
			last.text += event.Text
			last.reasoning += event.Text
			return
		}
		m.blocks = append(m.blocks, block{kind: blockThinking, running: true, text: event.Text, reasoning: event.Text})
	case agent.EventReasoned:
		if last := m.lastBlock(); last != nil && last.kind == blockThinking {
			last.running = false
			last.seconds = event.ToolMillis / 1000
			if strings.TrimSpace(event.Text) != "" {
				last.reasoning = event.Text
			}
			return
		}
		m.blocks = append(m.blocks, block{kind: blockThinking, reasoning: event.Text, seconds: event.ToolMillis / 1000})
	case agent.EventText:
		closeOpenThinking(m.blocks)
		if last := m.lastBlock(); last != nil && last.kind == blockAssistant {
			last.text += event.Text
			return
		}
		m.blocks = append(m.blocks, block{kind: blockAssistant, text: event.Text})
	case agent.EventToolStart:
		closeOpenThinking(m.blocks)
		m.blocks = append(m.blocks, block{
			kind:      blockTool,
			running:   true,
			toolName:  event.ToolName,
			toolLabel: event.ToolLabel,
			toolArgs:  event.ToolArgs,
		})
	case agent.EventToolEnd:
		if target := m.lastRunningTool(event.ToolName); target != nil {
			target.running = false
			target.toolOK = event.ToolOK
			target.toolLabel = event.ToolLabel
			target.toolMillis = event.ToolMillis
			target.toolResult = event.ToolResult
			return
		}
		m.blocks = append(m.blocks, block{
			kind:       blockTool,
			toolName:   event.ToolName,
			toolLabel:  event.ToolLabel,
			toolOK:     event.ToolOK,
			toolMillis: event.ToolMillis,
			toolResult: event.ToolResult,
		})
	case agent.EventPlan:
		m.applyPlan(event.Plan)
	case agent.EventNotice:
		m.blocks = append(m.blocks, block{kind: blockNotice, text: event.Text})
	case agent.EventError:
		if event.Err != nil {
			m.blocks = append(m.blocks, block{kind: blockError, text: event.Err.Error()})
		}
	case agent.EventTurnEnd:
		closeOpenThinking(m.blocks)
		switch event.StopReason {
		case "step-cap":
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "Stopped at the step budget. Reply to continue."})
		case "error":
			// The error event already rendered the message.
		}
	}
}

func (m *Model) applyPlan(plan []agent.Todo) {
	if last := m.lastBlock(); last != nil && last.kind == blockPlan {
		last.plan = plan
		return
	}
	m.blocks = append(m.blocks, block{kind: blockPlan, plan: plan})
}

func (m *Model) lastBlock() *block {
	if len(m.blocks) == 0 {
		return nil
	}
	return &m.blocks[len(m.blocks)-1]
}

func (m *Model) lastRunningTool(name string) *block {
	for index := len(m.blocks) - 1; index >= 0; index-- {
		item := &m.blocks[index]
		if item.kind == blockTool && item.running && item.toolName == name {
			return item
		}
	}
	return nil
}

func closeOpenThinking(blocks []block) {
	for index := range blocks {
		if blocks[index].kind == blockThinking {
			blocks[index].running = false
		}
	}
}

// enterChat returns the model to the chat composer with focus restored.
//
// Every path that leaves the setup wizard, a picker or a dialog must go
// through here. The wizard uses m.input while chat uses m.composer, so a
// direct mode switch without refocusing leaves the composer blurred and the
// operator cannot type.
func (m *Model) enterChat() tea.Cmd {
	m.current = modeChat
	m.input.Blur()
	m.input.EchoMode = textinput.EchoNormal
	m.composer.Focus()
	m.refresh()
	return textarea.Blink
}

// layout recomputes the viewport and composer for the window size. The fixed
// rows are the header, the status line, the hints and the composer with its
// border; everything else belongs to the transcript.
func (m *Model) layout() {
	transcriptHeight := m.height - (composerHeight + 6)
	if transcriptHeight < 5 {
		transcriptHeight = 5
	}
	m.viewport.Width = m.width
	m.viewport.Height = transcriptHeight
	// The border and padding are the textarea's own, so it reserves their
	// width. SetWidth therefore takes the full terminal width and the rendered
	// box is exactly that wide.
	m.applyComposerStyle()
	m.composer.SetWidth(m.width)
	m.input.Width = max(20, m.width-8)
	m.refresh()
}

// frameLogCap bounds the frame log: enough turns to compare against a
// screenshot, small enough to never matter on disk.
const frameLogCap = 300

// frameLogPath is set from TERMIXGO_FRAMELOG. When present, every transcript
// paint appends its plain text, so a garbled screen can be compared against
// the exact bytes the program handed to the renderer.
func frameLogPath() string { return strings.TrimSpace(os.Getenv("TERMIXGO_FRAMELOG")) }

// paintMinInterval is the fastest the transcript repaints during a burst of
// streamed deltas. Structural changes (tool boundaries, notices, errors)
// always paint at once; only running text is paced.
const paintMinInterval = 120 * time.Millisecond

// maybeRefresh paints now, or later when streaming and the last paint was
// recent. State always applies first: a skipped paint only delays pixels,
// and the tick plus any structural event catches up within a blink.
func (m *Model) maybeRefresh(streaming bool) {
	if !streaming || time.Since(m.lastPaint) >= paintMinInterval {
		m.refresh()
	}
}

// refresh re-renders the transcript into the viewport. When the operator is
// scrolled up, the offset is kept so reading history is not yanked away by
// new output; otherwise the view follows the bottom.
func (m *Model) refresh() {
	m.lastPaint = time.Now()
	m.applyComposerStyle()
	follow := m.viewport.AtBottom()
	content := transcript(m.blocks, m.styles, m.viewport.Width, m.showDetails)
	m.viewport.SetContent(content)
	if follow {
		m.viewport.GotoBottom()
	}
	logFrame(content)
}

// logFrame appends one painted transcript to the frame log when recording.
// The counter caps the file; beyond it recording stops silently rather than
// growing through a long session.
func logFrame(content string) {
	path := frameLogPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	var frames int
	if err == nil {
		frames = strings.Count(string(data), "\n--- frame ")
	}
	if frames >= frameLogCap {
		return
	}
	entry := fmt.Sprintf("\n--- frame %d ---\n%s\n", frames+1, stripANSI(content))
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(entry)
}

// View implements tea.Model.
//
// The returned frame is clamped to the terminal. Bubble Tea drops the top lines
// of a frame taller than the terminal and repositions the cursor relative to the
// previous frame, so a single row too many shifts the whole screen: the operator
// sees earlier lines in the wrong place, which reads as random and reversed text.
// Every surface also bounds its own rows; this is the invariant that catches a
// new one.
//
// The two guard messages bypass the clamp on purpose. "Loading" and "Terminal
// too small" are the whole screen, and truncating the sizing hint would hide the
// one sentence that says how to fix the window.
func (m *Model) View() string {
	if !m.ready {
		return "Loading Termixgo..."
	}
	if m.width < m.limits.MinWidth || m.height < m.limits.MinHeight {
		return fmt.Sprintf("Terminal too small. Resize to at least %dx%d.", m.limits.MinWidth, m.limits.MinHeight)
	}
	return fitFrame(m.screen(), m.width, m.height)
}

// fitFrame truncates a rendered frame to the terminal box.
func fitFrame(view string, width, height int) string {
	if width <= 0 || height <= 0 {
		return view
	}
	lines := strings.Split(view, "\n")
	for index, line := range lines {
		lines[index] = truncate(line, width)
	}
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	return strings.Join(lines, "\n")
}

// screen renders the current mode.
func (m *Model) screen() string {
	if m.current == modeHelp {
		return m.viewHelp()
	}
	if m.current == modeSetup {
		return m.viewSetup()
	}
	if m.current == modePicker || m.current == modeSessions {
		return m.viewPicker()
	}

	sections := []string{
		m.viewHeader(),
		m.viewport.View(),
		m.viewStatus(),
	}
	if m.pendingApproval != nil {
		sections = append(sections, m.viewApproval())
	} else if m.pendingAsk != nil {
		sections = append(sections, m.viewAsk())
	} else if len(m.slashMatches) > 0 {
		sections = append(sections, m.viewSlashMenu())
	} else if len(m.mentionMatches) > 0 {
		sections = append(sections, m.viewMentionMenu())
	} else {
		sections = append(sections, m.viewComposer(), m.viewHints())
	}
	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func (m *Model) viewHeader() string {
	trustWord := "untrusted"
	trustStyle := m.styles.StatusWarn
	if m.app.Trusted() {
		trustWord = "trusted"
		trustStyle = m.styles.StatusTrust
	}
	label := "Termixgo "
	workspace := m.app.Workspace()
	right := fmt.Sprintf("%s / %s", m.app.ModelLabel(), m.app.CurrentModel().Provider)

	// The header must never be wider than the terminal: a longer line wraps in
	// the terminal and pushes the rest of the frame down, which is what made
	// the screen look garbled. One column is held back so the two halves never
	// touch.
	available := max(1, m.width-1)
	if lipgloss.Width(label)+lipgloss.Width(workspace)+1+lipgloss.Width(trustWord)+lipgloss.Width(right) > available {
		// The status line repeats the model, so this is the half to drop.
		right = ""
	}
	room := available - lipgloss.Width(right) - lipgloss.Width(label) - 1 - lipgloss.Width(trustWord)
	if room < 0 {
		room = 0
	}
	workspace = truncateLeft(workspace, room)
	left := m.styles.StatusKey.Render(label) + m.styles.StatusValue.Render(workspace) + " " + trustStyle.Render(trustWord)

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) viewStatus() string {
	done, total := m.app.Todos().Progress()
	usage := m.app.Usage()
	spend, known := m.app.Cost()

	// The vitals are the numbers the operator watches while a turn runs, so they
	// are built first and always kept on screen. A narrow terminal drops the
	// session id and the approval label instead: both are repeated in the header
	// and in /status, while the token count and the spend have nowhere else to go.
	vitals := []string{m.styles.Dim.Render(fmt.Sprintf("tokens %d", usage.TotalTokens))}
	costStyle := m.styles.Dim
	if limit := m.appConfig().CostBudgetUSD; limit > 0 && spend >= limit {
		// Over budget is the one spend figure that should shout.
		costStyle = m.styles.StatusWarn
	}
	if known {
		vitals = append(vitals, costStyle.Render(fmt.Sprintf("$%.4f", spend)))
	} else {
		// A model with no price cannot be budgeted, and saying so is better than
		// a zero that reads as "free".
		vitals = append(vitals, costStyle.Render("cost n/a"))
	}
	if m.running && !m.hintsVisible() {
		// The hints row carries the live working indicator while it is on screen,
		// so repeating it here would print the same elapsed time above and below
		// the composer. A menu, an approval prompt or a question replaces that
		// row, and only then does the status line become the one place the
		// indicator can live.
		elapsed := time.Since(m.runStarted).Round(time.Second)
		vitals = append([]string{m.styles.Plan.Render(fmt.Sprintf("%s working %s", m.spin.View(), elapsed))}, vitals...)
	}

	// Everything else, most useful first: the drop loop below removes from the
	// end, so the context usage survives longer than the session id.
	identity := []string{m.styles.Dim.Render(m.app.ContextUsage())}
	if total > 0 {
		identity = append(identity, m.styles.Plan.Render(fmt.Sprintf("plan %d/%d", done, total)))
	}
	// The task in flight is named rather than only counted, so a glance at the
	// status line says what the agent is doing without scrolling the transcript.
	if active, ok := m.app.Todos().Active(); ok {
		identity = append(identity, m.styles.Plan.Render("task "+truncate(active.Title, 40)))
	}
	identity = append(identity,
		m.styles.Dim.Render("session "+shortID(m.app.Session().ID())),
		m.styles.Dim.Render(fmt.Sprintf("turns %d", m.app.Session().Turns())),
		m.styles.Dim.Render("approval "+string(m.app.Config().ApprovalMode)),
	)
	if count := m.queuedCount(); count > 0 {
		identity = append(identity, m.styles.Dim.Render(fmt.Sprintf("queue %d", count)))
	}
	// A scrolled-up transcript stops following new output, so the status names the
	// way back instead of leaving the operator wondering why the view went still.
	if !m.viewport.AtBottom() {
		identity = append(identity, m.styles.Notice.Render("scrolled (End follows)"))
	}
	if m.notice != "" {
		identity = append(identity, m.styles.Notice.Render(m.notice))
	}
	return m.composeStatus(identity, vitals)
}

// composeStatus joins the identity parts with the vitals, keeping the numbers on
// screen.
//
// The identity parts are added from the front, most useful first, and only while
// they fit beside the vitals. When even the vitals alone are too wide, they are
// dropped from the front too: the working indicator goes first, because the token
// count and the spend are the two figures that appear nowhere else on screen.
func (m *Model) composeStatus(identity, vitals []string) string {
	separator := m.styles.Dim.Render(" | ")
	sepWidth := lipgloss.Width(separator)

	for len(vitals) > 1 && lipgloss.Width(strings.Join(vitals, separator)) > m.width {
		vitals = vitals[1:]
	}
	vitalsText := strings.Join(vitals, separator)
	vitalsWidth := lipgloss.Width(vitalsText)

	room := m.width - vitalsWidth - sepWidth
	var kept []string
	for _, part := range identity {
		trial := append(append([]string{}, kept...), part)
		if lipgloss.Width(strings.Join(trial, separator)) > room {
			break
		}
		kept = trial
	}
	if len(kept) == 0 {
		return truncate(vitalsText, max(1, m.width))
	}

	identityText := strings.Join(kept, separator)
	// Right-aligning the vitals keeps their position stable as the identity text
	// grows and shrinks, so the numbers do not jump around during a run.
	gap := m.width - lipgloss.Width(identityText) - vitalsWidth
	if gap < sepWidth {
		return truncate(identityText+separator+vitalsText, max(1, m.width))
	}
	return identityText + strings.Repeat(" ", gap) + vitalsText
}

func shortID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[:6]
}

func (m *Model) viewComposer() string {
	return m.composer.View()
}

// applyComposerStyle moves the composer's border between its idle and busy
// colour. The border lives on the textarea's own base style so its width is
// reserved when the text wraps; painting it afterwards is what made a wrapped
// line spill onto the next row.
func (m *Model) applyComposerStyle() {
	base := m.styles.Composer
	if m.running {
		base = m.styles.ComposerBusy
	}
	m.composer.FocusedStyle.Base = base
	m.composer.BlurredStyle.Base = base
}

// hintsVisible reports whether the row that carries the live working
// indicator is on screen. Every surface that replaces the composer and the
// hints, which is a menu, an approval prompt or a question, takes that row
// away, so the status line has to know whether it is still there.
func (m *Model) hintsVisible() bool {
	return m.pendingApproval == nil && m.pendingAsk == nil &&
		len(m.slashMatches) == 0 && len(m.mentionMatches) == 0
}

func (m *Model) viewHints() string {
	text := ""
	if m.running {
		elapsed := time.Since(m.runStarted).Round(time.Second)
		if count := m.queuedCount(); count > 0 {
			text = fmt.Sprintf(" %s working (%s) | %d queued | Enter steers the run | Ctrl+O details | Esc stop | Ctrl+C quit", m.spin.View(), elapsed, count)
		} else {
			text = fmt.Sprintf(" %s working (%s) | Enter steers the run | Ctrl+O details | Esc stop | Ctrl+C quit", m.spin.View(), elapsed)
		}
	} else {
		text = " Enter send | Ctrl+J newline | / commands @ files | Tab complete | PgUp/PgDn scroll | Ctrl+O details | Ctrl+C quit"
	}
	// A hint longer than the terminal would wrap and push the frame down, so it
	// is clipped rather than allowed to reflow the whole screen.
	return m.styles.Hint.Render(truncate(text, max(1, m.width)))
}

func (m *Model) viewSlashMenu() string {
	body := m.menuBodyWidth()
	var rows []string
	total := len(m.slashMatches) + 1
	start, end := windowRows(m.slashCursor, total, m.menuRowLimit())
	if start > 0 {
		rows = append(rows, truncate(m.styles.MenuDesc.Render(fmt.Sprintf("  ... %d above", start)), body))
	}
	for index := start; index < end && index < total; index++ {
		// The last row is the way back to typing for operators who opened the
		// menu by accident or navigated past their command.
		if index == len(m.slashMatches) {
			if index == m.slashCursor {
				rows = append(rows, truncate(m.styles.MenuSelected.Render("> <- back")+"  "+m.styles.MenuDesc.Render("Return to typing"), body))
			} else {
				rows = append(rows, truncate("  "+m.styles.MenuDesc.Render("<- back  Return to typing"), body))
			}
			continue
		}
		command := m.slashMatches[index]
		usage := command.Trigger
		if command.Args != "" {
			usage += " " + command.Args
		}
		if index == m.slashCursor {
			rows = append(rows, truncate(m.styles.MenuSelected.Render("> "+usage)+"  "+m.styles.MenuDesc.Render(command.Summary), body))
			continue
		}
		rows = append(rows, truncate("  "+m.styles.MenuKey.Render(usage)+"  "+m.styles.MenuDesc.Render(command.Summary), body))
	}
	if end < total {
		rows = append(rows, truncate(m.styles.MenuDesc.Render(fmt.Sprintf("  ... %d more (type to filter)", total-end)), body))
	}
	rows = append(rows, truncate(m.styles.Hint.Render("Up/Down move | Tab arguments | Enter run | Esc back"), body))
	// No Width() here: the Menu style adds one column of padding on each side
	// on top of whatever Width constrains, which wraps an exactly-sized row.
	// Truncating every row to width-2 and letting the padding fill the rest is
	// what keeps the padded block at the terminal width.
	return m.styles.Menu.Render(strings.Join(rows, "\n"))
}

// menuBodyWidth is the content width a menu row may use. The Menu style adds
// one column of padding on each side, so the content has to be two columns
// narrower than the terminal or the padded block overflows and wraps.
func (m *Model) menuBodyWidth() int {
	return max(1, m.width-2)
}

// maxMenuRows is the ceiling for a list overlay's visible rows.
const maxMenuRows = 14

// menuRowLimit is how many list rows this terminal can hold. It adapts to the
// height so a tall window shows a long list, while a short one still fits:
// the header, the status line, the hint row and the box frame keep their own.
func (m *Model) menuRowLimit() int {
	const furniture = 6 + 4
	limit := m.height - furniture
	if limit < 4 {
		limit = 4
	}
	if limit > maxMenuRows {
		limit = maxMenuRows
	}
	return limit
}

// windowRows returns the slice bounds for a list whose cursor must stay
// visible, and whether the list was clipped.
func windowRows(cursor, total, limit int) (start, end int) {
	if total <= limit {
		return 0, total
	}
	start = cursor - limit/2
	if start < 0 {
		start = 0
	}
	if start+limit > total {
		start = total - limit
	}
	return start, start + limit
}

// viewMentionMenu lists the @file candidates under the composer. Tab takes
// the highlighted file into the text.
func (m *Model) viewMentionMenu() string {
	var rows []string
	total := len(m.mentionMatches)
	start, end := windowRows(m.mentionCursor, total, m.menuRowLimit())
	if start > 0 {
		rows = append(rows, truncate(m.styles.MenuDesc.Render(fmt.Sprintf("  ... %d above", start)), m.menuBodyWidth()))
	}
	for index := start; index < end; index++ {
		path := m.mentionMatches[index]
		if index == m.mentionCursor {
			rows = append(rows, truncate(m.styles.MenuSelected.Render("> @"+path), m.menuBodyWidth()))
			continue
		}
		rows = append(rows, truncate("  "+m.styles.MenuKey.Render("@"+path), m.menuBodyWidth()))
	}
	if end < total {
		rows = append(rows, truncate(m.styles.MenuDesc.Render(fmt.Sprintf("  ... %d more", total-end)), m.menuBodyWidth()))
	}
	rows = append(rows, m.styles.Hint.Render("Tab complete | Up/Down move | Esc cancel"))
	// Rows are truncated to width-2 above so the Menu padding brings the block
	// to the terminal width without wrapping.
	return m.styles.Menu.Render(strings.Join(rows, "\n"))
}

func (m *Model) viewApproval() string {
	request := m.pendingApproval
	body := []string{
		m.styles.BoxTitle.Render("Approval needed"),
		"",
		m.styles.StatusValue.Render(request.Tool) + m.styles.Dim.Render(" ("+request.Risk+")"),
		m.styles.Dim.Render(truncate(request.Detail, m.width-8)),
	}
	// The diff budget is whatever the terminal has left after the fixed rows
	// and the options, so the dialog never grows the frame past the terminal.
	diffLines := m.overlayRowBudget()
	if diff := renderApprovalDiff(request.Diff, m.styles, m.width-8, diffLines); diff != "" {
		body = append(body, "", diff)
	}
	body = append(body, "")
	for index, option := range approvalOptions {
		row := m.styles.MenuKey.Render(option.key) + " " + option.label
		if index == m.approvalCursor {
			body = append(body, m.styles.MenuSelected.Render("> "+row))
			continue
		}
		body = append(body, "  "+row)
	}
	body = append(body, "", m.styles.Hint.Render("Up/Down move | Enter select | Esc deny"))
	return m.styles.Box.Width(m.width - 6).Render(strings.Join(body, "\n"))
}

// overlayRowBudget is how many body rows an overlay may draw.
//
// The transcript and the frame furniture keep their rows, so an overlay takes
// only what is left. Without this the approval dialog's diff and the question's
// options made the frame taller than the terminal, and the renderer then dropped
// the frame's top lines and shifted the cursor, which scrambles the screen.
func (m *Model) overlayRowBudget() int {
	// The Box style adds one row of padding on each side plus the border, and
	// the header, status and hints keep their own rows.
	const furniture = 2 + 2 + 1 + 3
	budget := m.height - (composerHeight + furniture)
	if budget < 3 {
		budget = 3
	}
	if budget > 14 {
		budget = 14
	}
	return budget
}

// renderApprovalDiff colours a unified preview for the approval dialog.
// Lines are clipped to the width so a long file cannot break the box, and the
// count is bounded so the dialog cannot grow past the terminal.
func renderApprovalDiff(diff string, styles Styles, width, maxLines int) string {
	diff = strings.TrimSpace(diff)
	if diff == "" {
		return ""
	}
	if maxLines < 1 {
		maxLines = 1
	}
	lines := strings.Split(diff, "\n")
	shown := lines
	truncated := false
	if len(lines) > maxLines {
		shown = lines[:maxLines]
		truncated = true
	}
	var out []string
	for _, line := range shown {
		line = truncate(line, max(1, width))
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			out = append(out, styles.ToolDone.Render(line))
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			out = append(out, styles.ToolError.Render(line))
		default:
			out = append(out, styles.Dim.Render(line))
		}
	}
	if truncated {
		out = append(out, styles.Dim.Render(fmt.Sprintf("... (%d more lines)", len(lines)-maxLines)))
	}
	return strings.Join(out, "\n")
}

func (m *Model) viewAsk() string {
	body := []string{
		m.styles.BoxTitle.Render("The agent has a question"),
		"",
		m.styles.StatusValue.Render(m.pendingAsk.question),
	}
	if len(m.pendingAsk.options) > 0 {
		body = append(body, "")
		options := m.pendingAsk.options
		if len(options) > m.menuRowLimit() {
			options = options[:m.menuRowLimit()]
		}
		for index, option := range options {
			body = append(body, m.styles.MenuKey.Render(fmt.Sprintf("%d", index+1))+". "+option)
		}
		if len(m.pendingAsk.options) > len(options) {
			body = append(body, m.styles.MenuDesc.Render(fmt.Sprintf("... %d more options", len(m.pendingAsk.options)-len(options))))
		}
	}
	body = append(body, "", m.input.View(), m.styles.Hint.Render("Enter answer | Esc decline"))
	return m.styles.Box.Width(m.width - 6).Render(strings.Join(body, "\n"))
}

func (m *Model) viewHelp() string {
	entries := SlashHelp()
	// The box draws a border and one padding row on each side, so four rows of
	// the terminal belong to the frame and the rest is body.
	budget := m.height - 4
	if budget < 6 {
		budget = 6
	}

	header := []string{m.styles.BoxTitle.Render("Commands"), ""}
	keys := []string{
		"", m.styles.BoxTitle.Render("Keys"), "",
		m.styles.MenuDesc.Render("  Enter send | Ctrl+J newline | Tab complete | PgUp/PgDn scroll"),
		m.styles.MenuDesc.Render("  Esc stop or clear | Ctrl+O toggle details | Ctrl+C quit"),
	}
	trailer := []string{m.styles.Hint.Render(fmt.Sprintf("Press any key to close. %d commands in total.", len(entries)))}

	// The key list is the part to drop on a short terminal: the commands are
	// what the operator opened help for.
	limit := budget - len(header) - len(keys) - len(trailer)
	showKeys := limit >= 3
	if !showKeys {
		limit = budget - len(header) - len(trailer)
	}
	if limit < 1 {
		limit = 1
	}
	if limit > m.menuRowLimit() {
		limit = m.menuRowLimit()
	}

	lines := append([]string{}, header...)
	start, end := windowRows(0, len(entries), limit)
	if start > 0 {
		lines = append(lines, m.styles.MenuDesc.Render(fmt.Sprintf("  ... %d above", start)))
	}
	for _, entry := range entries[start:end] {
		parts := strings.SplitN(entry, "\t", 2)
		usage := parts[0]
		summary := ""
		if len(parts) > 1 {
			summary = parts[1]
		}
		lines = append(lines, m.styles.MenuKey.Render(fmt.Sprintf("  %-16s", usage))+m.styles.MenuDesc.Render(summary))
	}
	if end < len(entries) {
		lines = append(lines, m.styles.MenuDesc.Render(fmt.Sprintf("  ... %d more, typed commands still work", len(entries)-end)))
	}
	if len(m.custom) > 0 {
		lines = append(lines, "", m.styles.BoxTitle.Render("Custom"))
		for _, item := range m.custom {
			lines = append(lines, m.styles.MenuKey.Render(fmt.Sprintf("  %-16s", "/"+item.Name))+m.styles.MenuDesc.Render(item.Description))
		}
	}
	if showKeys {
		lines = append(lines, keys...)
	}
	lines = append(lines, trailer...)
	return m.styles.Box.Width(m.width - 4).Render(strings.Join(lines, "\n"))
}

// customTTL bounds how often the command files are re-read. Discovery hits
// the disk, so the menu reuses the cache within the window and a new file
// still appears seconds later without a restart.
const customTTL = 10 * time.Second

// ensureCustomCommands refreshes the user-defined slash commands when the
// cache is stale. Failures keep the previous set: a broken file must not
// hide the commands that still parse.
func (m *Model) ensureCustomCommands() {
	if !m.customAt.IsZero() && time.Since(m.customAt) < customTTL {
		return
	}
	_ = m.reloadCustomCommands()
}

// reloadCustomCommands re-reads the command files now.
func (m *Model) reloadCustomCommands() error {
	commands, err := command.Discover(m.app.Workspace())
	if err != nil {
		return err
	}
	m.custom = commands
	m.customAt = time.Now()
	return nil
}

// matchCustom returns the user-defined commands matching a typed prefix.
func (m *Model) matchCustom(input string) []SlashCommand {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") || strings.ContainsAny(trimmed, " \n\t") {
		return nil
	}
	var matches []SlashCommand
	for _, item := range m.custom {
		trigger := "/" + item.Name
		if strings.HasPrefix(trigger, strings.ToLower(trimmed)) {
			matches = append(matches, SlashCommand{Trigger: trigger, Summary: item.Description})
		}
	}
	return matches
}

// updateSlashMatches recomputes the inline command menu.
func (m *Model) updateSlashMatches() {
	m.ensureCustomCommands()
	matches := MatchSlash(m.composer.Value())
	matches = append(matches, m.matchCustom(m.composer.Value())...)
	// The highlight follows the first match whenever the set itself changes.
	// Comparing lengths is not enough: "/model" and "/harness" both match
	// exactly one command, and a cursor parked on the back row would survive
	// the swap and make Enter close the menu instead of running anything.
	if slashSignature(matches) != slashSignature(m.slashMatches) {
		m.slashCursor = 0
	}
	// The cursor may rest on the back row past the end, which stays valid
	// while the match set is unchanged.
	if m.slashCursor > len(matches) {
		m.slashCursor = 0
	}
	m.slashMatches = matches
	m.updateMentionMatches()
}

// slashSignature identifies a menu set for the cursor: same length with
// different entries is still a different menu.
func slashSignature(matches []SlashCommand) string {
	triggers := make([]string, 0, len(matches))
	for _, match := range matches {
		triggers = append(triggers, match.Trigger)
	}
	return strings.Join(triggers, "\n")
}

// updateMentionMatches recomputes the @file menu from the token being typed.
func (m *Model) updateMentionMatches() {
	token, at, ok := parseMention(m.composer.Value())
	if !ok {
		m.mentionMatches = nil
		m.mentionCursor = 0
		return
	}
	matches := mentionCandidates(m.app.Workspace(), token, mentionMatchCap)
	if strings.Join(matches, "\n") != strings.Join(m.mentionMatches, "\n") {
		m.mentionCursor = 0
	}
	if m.mentionCursor >= len(matches) {
		m.mentionCursor = 0
	}
	m.mentionMatches = matches
	m.mentionAt = at
}

// approvalTimeout bounds how long the agent goroutine waits for the operator.
// It exists because an operator who walks away must not leave a turn parked
// forever, and denying is the safe answer.
const approvalTimeout = 10 * time.Minute

// awaitReply waits for a value from the UI, falling back on timeout. It is
// shared by the approval and question handshakes, which differ only in the
// type they carry and the answer that is safe to assume.
func awaitReply[T any](reply <-chan T, fallback T, timeout time.Duration) T {
	select {
	case value := <-reply:
		return value
	case <-time.After(timeout):
		return fallback
	}
}

// Approve implements app.Interactor and blocks the agent goroutine until the
// operator answers in the UI.
func (m *Model) Approve(request agent.ApprovalRequest) agent.Decision {
	if m.program == nil {
		return agent.DecisionDeny
	}
	reply := make(chan agent.Decision, 1)
	m.program.Send(approvalRequestMsg{request: request, reply: reply})
	return awaitReply(reply, agent.DecisionDeny, approvalTimeout)
}

// Ask implements app.Interactor.
func (m *Model) Ask(question string, options []string) (string, error) {
	if m.program == nil {
		return "", fmt.Errorf("the interface is not attached")
	}
	reply := make(chan string, 1)
	m.program.Send(askRequestMsg{question: question, options: options, reply: reply})
	answer := awaitReply(reply, "", approvalTimeout)
	if answer == "" {
		return "", fmt.Errorf("the operator did not answer")
	}
	return answer, nil
}

// appConfig exposes the config for the slash handlers.
func (m *Model) appConfig() config.Config { return m.app.Config() }

// providerModels lists the catalogue for the model picker.
func (m *Model) providerModels() []provider.Model { return provider.Models() }
