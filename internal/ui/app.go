package ui

import (
	"context"
	"fmt"
	"io"
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
	pendingAsk     *askRequestMsg
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

// New builds the program model for an app.
func New(application *app.App) *Model { return NewWithOptions(application, Options{}) }

// NewWithOptions builds the program model with explicit start options.
func NewWithOptions(application *app.App, options Options) *Model {
	styles := NewStyles(DefaultPalette())

	composer := textarea.New()
	composer.Placeholder = "Ask Termixgo to change something, or type /help"
	composer.ShowLineNumbers = false
	composer.CharLimit = 32000
	composer.SetHeight(3)
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
	return tea.Batch(textarea.Blink, m.spin.Tick)
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
		m.applyEvent(agent.Event(typed))
		m.refresh()
		return m, waitForEvent(m.app.Events())

	case tickMsg:
		if m.running {
			m.refresh()
			return m, tea.Batch(m.spin.Tick, tick())
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(typed)
		return m, cmd

	case runDoneMsg:
		m.running = false
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
		return m, nil

	case approvalRequestMsg:
		request := typed.request
		m.pendingApproval = &request
		m.approvalReply = typed.reply
		m.approvalCursor = 0
		m.refresh()
		return m, nil

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
		m.composer.SetValue("")
		m.slashMatches = nil
		m.mentionMatches = nil
		m.refresh()
		return m, nil
	case "enter":
		value := strings.TrimSpace(m.composer.Value())
		if value == "" {
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
		if slashName, _, ok := ParseSlash(value); ok {
			// Most slash commands typed mid-run queue behind the turn and
			// run on drain, which keeps one turn in flight. Only the
			// controls that must act on the live turn bypass the queue:
			// /stop cancels it and /exit quits at once. Queueing those
			// would defer them past the moment they are needed.
			if m.running && (slashName == "stop" || slashName == "exit" || slashName == "quit") {
				m.composer.SetValue("")
				m.slashMatches = nil
				m.mentionMatches = nil
				return m.submit(value)
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
			m.slashCursor = (m.slashCursor - 1 + len(m.slashMatches)) % len(m.slashMatches)
			return m, nil
		}
	case "down":
		if len(m.mentionMatches) > 0 {
			m.mentionCursor = (m.mentionCursor + 1) % len(m.mentionMatches)
			return m, nil
		}
		if len(m.slashMatches) > 0 {
			m.slashCursor = (m.slashCursor + 1) % len(m.slashMatches)
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
			m.composer.SetValue(m.slashMatches[m.slashCursor].Trigger + " ")
			m.slashMatches = nil
			return m, nil
		}
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
	return m, nil
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
	if m.running && m.app.Running() {
		m.app.Steer(value)
	} else {
		m.queue = append(m.queue, value)
	}
	m.composer.SetValue("")
	m.slashMatches = nil
	m.mentionMatches = nil
	m.notice = fmt.Sprintf("Queued (%d). The agent picks it up at its next step.", m.queuedCount())
	m.refresh()
	return m, nil
}

// queuedCount is everything still waiting: what the UI holds plus what the
// live turn has been handed but not yet taken.
func (m *Model) queuedCount() int { return len(m.queue) + m.app.SteerCount() }

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
	return m, tea.Batch(m.spin.Tick, tick(), waitForEvent(application.Events()))
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

// layout recomputes the viewport and composer for the window size.
func (m *Model) layout() {
	transcriptHeight := m.height - 9
	if transcriptHeight < 5 {
		transcriptHeight = 5
	}
	m.viewport.Width = m.width
	m.viewport.Height = transcriptHeight
	m.composer.SetWidth(max(20, m.width-4))
	m.input.Width = max(20, m.width-8)
	m.refresh()
}

// refresh re-renders the transcript into the viewport and keeps it scrolled.
func (m *Model) refresh() {
	content := transcript(m.blocks, m.styles, m.viewport.Width, m.showDetails)
	m.viewport.SetContent(content)
	m.viewport.GotoBottom()
}

// View implements tea.Model.
func (m *Model) View() string {
	if !m.ready {
		return "Loading Termixgo..."
	}
	if m.width < m.limits.MinWidth || m.height < m.limits.MinHeight {
		return fmt.Sprintf("Terminal too small. Resize to at least %dx%d.", m.limits.MinWidth, m.limits.MinHeight)
	}

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
	trust := m.styles.StatusWarn.Render("untrusted")
	if m.app.Trusted() {
		trust = m.styles.StatusTrust.Render("trusted")
	}
	left := m.styles.StatusKey.Render("Termixgo ") + m.styles.StatusValue.Render(m.app.Workspace()) + " " + trust
	right := m.styles.Dim.Render(fmt.Sprintf("%s / %s", m.app.ModelLabel(), m.app.CurrentModel().Provider))
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) viewStatus() string {
	done, total := m.app.Todos().Progress()
	usage := m.app.Usage()
	parts := []string{
		m.styles.Dim.Render(fmt.Sprintf("session %s", shortID(m.app.Session().ID()))),
		m.styles.Dim.Render(fmt.Sprintf("turns %d", m.app.Session().Turns())),
		m.styles.Dim.Render(m.app.ContextUsage()),
	}
	if total > 0 {
		parts = append(parts, m.styles.Plan.Render(fmt.Sprintf("plan %d/%d", done, total)))
	}
	// The task in flight is named rather than only counted, so a glance at the
	// status line says what the agent is doing without scrolling the transcript.
	if active, ok := m.app.Todos().Active(); ok {
		parts = append(parts, m.styles.Plan.Render("task "+truncate(active.Title, 40)))
	}
	if usage.TotalTokens > 0 {
		parts = append(parts, m.styles.Dim.Render(fmt.Sprintf("tokens %d", usage.TotalTokens)))
	}
	if spend, known := m.app.Cost(); known && spend > 0 {
		style := m.styles.Dim
		if limit := m.appConfig().CostBudgetUSD; limit > 0 && spend >= limit {
			style = m.styles.StatusWarn
		}
		parts = append(parts, style.Render(fmt.Sprintf("$%.4f", spend)))
	}
	parts = append(parts, m.styles.Dim.Render("approval "+string(m.app.Config().ApprovalMode)))
	if count := m.queuedCount(); count > 0 {
		parts = append(parts, m.styles.Dim.Render(fmt.Sprintf("queue %d", count)))
	}
	if m.notice != "" {
		parts = append(parts, m.styles.Notice.Render(m.notice))
	}
	return strings.Join(parts, m.styles.Dim.Render(" | "))
}

func shortID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[:6]
}

func (m *Model) viewComposer() string {
	style := m.styles.Composer
	if m.running {
		style = m.styles.ComposerBusy
	}
	prefix := m.styles.Prompt.Render("> ")
	body := m.composer.View()
	return style.Width(m.width - 2).Render(prefix + body)
}

func (m *Model) viewHints() string {
	if m.running {
		elapsed := time.Since(m.runStarted).Round(time.Second)
		if count := m.queuedCount(); count > 0 {
			return m.styles.Hint.Render(fmt.Sprintf(" %s working (%s) | %d queued | Enter steers the run | Ctrl+O details | Esc stop | Ctrl+C quit", m.spin.View(), elapsed, count))
		}
		return m.styles.Hint.Render(fmt.Sprintf(" %s working (%s) | Enter steers the run | Ctrl+O details | Esc stop | Ctrl+C quit", m.spin.View(), elapsed))
	}
	return m.styles.Hint.Render(" Enter send | Ctrl+J newline | / commands @ files | Tab complete | Ctrl+O details | Ctrl+C quit")
}

func (m *Model) viewSlashMenu() string {
	var rows []string
	for index, command := range m.slashMatches {
		usage := command.Trigger
		if command.Args != "" {
			usage += " " + command.Args
		}
		if index == m.slashCursor {
			rows = append(rows, m.styles.MenuSelected.Render("> "+usage)+"  "+m.styles.MenuDesc.Render(command.Summary))
			continue
		}
		rows = append(rows, "  "+m.styles.MenuKey.Render(usage)+"  "+m.styles.MenuDesc.Render(command.Summary))
	}
	return m.styles.Menu.Render(strings.Join(rows, "\n"))
}

// viewMentionMenu lists the @file candidates under the composer. Tab takes
// the highlighted file into the text.
func (m *Model) viewMentionMenu() string {
	var rows []string
	for index, path := range m.mentionMatches {
		if index == m.mentionCursor {
			rows = append(rows, m.styles.MenuSelected.Render("> @"+path))
			continue
		}
		rows = append(rows, "  "+m.styles.MenuKey.Render("@"+path))
	}
	rows = append(rows, m.styles.Hint.Render("Tab complete | Up/Down move | Esc cancel"))
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
	if diff := renderApprovalDiff(request.Diff, m.styles, m.width-8); diff != "" {
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

// renderApprovalDiff colours a unified preview for the approval dialog.
// Lines are clipped to the width so a long file cannot break the box.
func renderApprovalDiff(diff string, styles Styles, width int) string {
	diff = strings.TrimSpace(diff)
	if diff == "" {
		return ""
	}
	lines := strings.Split(diff, "\n")
	const maxLines = 14
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
		for index, option := range m.pendingAsk.options {
			body = append(body, m.styles.MenuKey.Render(fmt.Sprintf("%d", index+1))+". "+option)
		}
	}
	body = append(body, "", m.input.View(), m.styles.Hint.Render("Enter answer | Esc decline"))
	return m.styles.Box.Width(m.width - 6).Render(strings.Join(body, "\n"))
}

func (m *Model) viewHelp() string {
	lines := []string{
		m.styles.BoxTitle.Render("Commands"),
		"",
	}
	for _, entry := range SlashHelp() {
		parts := strings.SplitN(entry, "\t", 2)
		usage := parts[0]
		summary := ""
		if len(parts) > 1 {
			summary = parts[1]
		}
		lines = append(lines, m.styles.MenuKey.Render(fmt.Sprintf("  %-16s", usage))+m.styles.MenuDesc.Render(summary))
	}
	if len(m.custom) > 0 {
		lines = append(lines, "", m.styles.BoxTitle.Render("Custom"))
		for _, item := range m.custom {
			lines = append(lines, m.styles.MenuKey.Render(fmt.Sprintf("  %-16s", "/"+item.Name))+m.styles.MenuDesc.Render(item.Description))
		}
		lines = append(lines, m.styles.Hint.Render("  Add one at .termixgo/commands/<name>.md with $ARGUMENTS for the typed text."))
	}
	lines = append(lines, "",
		m.styles.BoxTitle.Render("Keys"),
		"",
		m.styles.MenuDesc.Render("  Enter send | Ctrl+J newline | Tab complete | Esc stop or clear | Ctrl+O toggle details | Ctrl+C quit"),
		"",
		m.styles.Hint.Render("Press any key to close."),
	)
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
	if len(matches) != len(m.slashMatches) {
		m.slashCursor = 0
	}
	if m.slashCursor >= len(matches) {
		m.slashCursor = 0
	}
	m.slashMatches = matches
	m.updateMentionMatches()
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
	if len(matches) != len(m.mentionMatches) {
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
