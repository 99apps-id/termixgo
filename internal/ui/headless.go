package ui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/app"
	customcmd "github.com/99apps-id/termixgo/internal/command"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/version"
)

// ANSI codes used by the plain mode. They are disabled when the output is not
// a terminal, which keeps piped output clean.
const (
	ansiReset   = "\x1b[0m"
	ansiDim     = "\x1b[2m"
	ansiCyan    = "\x1b[36m"
	ansiMagenta = "\x1b[35m"
	ansiYellow  = "\x1b[33m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
)

// plainPrinter writes agent events to a stream without a TUI.
type plainPrinter struct {
	out       io.Writer
	colored   bool
	reasoning bool
	lastLine  bool
	// failure is the first error event seen. The run loop reports a provider
	// failure as an event and still returns nil, so this is the only signal a
	// one-shot call has that the turn did not succeed.
	failure error
}

func newPlainPrinter(out io.Writer, colored bool) *plainPrinter {
	return &plainPrinter{out: out, colored: colored}
}

func (p *plainPrinter) color(code, text string) string {
	if !p.colored {
		return text
	}
	return code + text + ansiReset
}

// print renders one event.
func (p *plainPrinter) print(event agent.Event) {
	// Strip terminal control characters so a piped run's log cannot be made to
	// emit colour or cursor sequences from model or tool output.
	event.Text = sanitizeText(event.Text)
	event.ToolLabel = sanitizeText(event.ToolLabel)
	event.ToolResult = sanitizeText(event.ToolResult)
	switch event.Kind {
	case agent.EventThinking:
		if !p.reasoning {
			p.reasoning = true
			fmt.Fprintln(p.out)
			fmt.Fprint(p.out, p.color(ansiMagenta, "thinking "))
		}
		fmt.Fprint(p.out, p.color(ansiDim, event.Text))
	case agent.EventReasoned:
		if p.reasoning {
			p.reasoning = false
			fmt.Fprintln(p.out)
		}
	case agent.EventText:
		if p.reasoning {
			p.reasoning = false
			fmt.Fprintln(p.out)
		}
		fmt.Fprint(p.out, event.Text)
		p.lastLine = true
	case agent.EventToolStart:
		if p.lastLine {
			fmt.Fprintln(p.out)
			p.lastLine = false
		}
		fmt.Fprintln(p.out, p.color(ansiYellow, "  > "+event.ToolLabel))
	case agent.EventToolEnd:
		marker := p.color(ansiGreen, "  + ")
		if !event.ToolOK {
			marker = p.color(ansiRed, "  x ")
		}
		fmt.Fprintln(p.out, marker+event.ToolLabel)
	case agent.EventPlan:
		fmt.Fprintln(p.out)
		for _, todo := range event.Plan {
			mark := "[ ]"
			switch todo.Status {
			case "in_progress":
				mark = "[>]"
			case "completed":
				mark = "[x]"
			}
			fmt.Fprintf(p.out, "  %s %s\n", mark, todo.Title)
		}
	case agent.EventNotice:
		fmt.Fprintln(p.out)
		fmt.Fprintln(p.out, p.color(ansiDim, event.Text))
	case agent.EventError:
		if event.Err != nil {
			if p.failure == nil {
				p.failure = event.Err
			}
			fmt.Fprintln(p.out)
			fmt.Fprintln(p.out, p.color(ansiRed, "error: "+event.Err.Error()))
		}
	case agent.EventTurnEnd:
		if p.lastLine {
			fmt.Fprintln(p.out)
			p.lastLine = false
		}
	}
}

// plainInteractor answers approvals and questions on the terminal.
type plainInteractor struct {
	in  io.Reader
	out io.Writer
	// reader is built once and reused. A bufio.Reader buffers ahead of the line
	// it returns, so a fresh one per question throws away whatever else arrived
	// in the same read: over a pipe that is the operator's next request, which
	// then never runs.
	reader *bufio.Reader
}

// input returns the one reader this interactor reads questions from.
func (i *plainInteractor) input() *bufio.Reader {
	if i.reader == nil {
		i.reader = bufio.NewReader(i.in)
	}
	return i.reader
}

func (i *plainInteractor) readLine() (string, error) {
	line, err := i.input().ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (i *plainInteractor) Approve(request agent.ApprovalRequest) agent.Decision {
	fmt.Fprintf(i.out, "\nApproval needed: %s (%s)\n  %s\n", request.Tool, request.Risk, request.Detail)
	if diff := strings.TrimSpace(request.Diff); diff != "" {
		lines := strings.Split(diff, "\n")
		if len(lines) > 20 {
			lines = append(lines[:20], fmt.Sprintf("... (%d more lines)", len(lines)-20))
		}
		for _, line := range lines {
			fmt.Fprintf(i.out, "  %s\n", line)
		}
	}
	fmt.Fprint(i.out, "[y] once  [s] session  [a] always  [n] deny: ")
	answer, err := i.readLine()
	if err != nil {
		return agent.DecisionDeny
	}
	switch strings.ToLower(answer) {
	case "y", "yes":
		return agent.DecisionAllowOnce
	case "s":
		return agent.DecisionAllowSession
	case "a", "always":
		return agent.DecisionAllowAlways
	default:
		return agent.DecisionDeny
	}
}

func (i *plainInteractor) Ask(question string, options []string) (string, error) {
	fmt.Fprintf(i.out, "\nQuestion: %s\n", question)
	for index, option := range options {
		fmt.Fprintf(i.out, "  %d. %s\n", index+1, option)
	}
	fmt.Fprint(i.out, "Answer: ")
	answer, err := i.readLine()
	if err != nil {
		return "", err
	}
	if len(options) > 0 {
		if index := parseIndex(answer); index > 0 && index <= len(options) {
			return options[index-1], nil
		}
	}
	return answer, nil
}

func parseIndex(value string) int {
	number := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return -1
		}
		number = number*10 + int(r-'0')
	}
	if value == "" {
		return -1
	}
	return number
}

// RunPlain is the interactive mode used when there is no terminal for the TUI,
// for example over a pipe or in CI.
func RunPlain(application *app.App, in io.Reader, out io.Writer) error {
	return RunPlainWithContext(context.Background(), application, in, out)
}

// RunPlainWithContext is RunPlain under a caller-supplied context, which is
// what lets Ctrl+C cancel the turn in flight instead of killing the process.
func RunPlainWithContext(ctx context.Context, application *app.App, in io.Reader, out io.Writer) error {
	colored := isTerminalWriter(out)
	printer := newPlainPrinter(out, colored)
	application.SetInteractor(&plainInteractor{in: in, out: out})
	drain := drainEvents(application, printer)

	printPlainWelcome(application, out, colored)
	fmt.Fprintln(out)

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		if ctx.Err() != nil {
			break
		}
		fmt.Fprint(out, plainPrompt(colored))
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// A bare exit or quit leaves, because that is how a REPL ends and the
		// alternative is spending a model call on the word. The slash form still
		// works; anything else is a prompt.
		if strings.EqualFold(line, "exit") || strings.EqualFold(line, "quit") {
			break
		}
		if name, args, ok := ParseSlash(line); ok {
			quit, err := runPlainSlash(ctx, application, name, args, out)
			if err != nil {
				fmt.Fprintln(out, printer.color(ansiRed, "error: "+err.Error()))
			}
			if quit {
				break
			}
			continue
		}
		if err := application.RunTurn(ctx, ExpandMentions(application.Workspace(), line)); err != nil {
			fmt.Fprintln(out, printer.color(ansiRed, "error: "+err.Error()))
		}
		fmt.Fprintln(out)
	}
	drain()
	return nil
}

// drainEvents prints every agent event until the returned function is called.
//
// The returned function waits for the printer goroutine to finish, and the
// goroutine prints whatever is already queued before it returns. Without that
// a one-shot run could stop the drain before the answer was written, which
// loses the output rather than only the ordering.
func drainEvents(application *app.App, printer *plainPrinter) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case event := <-application.Events():
				printer.print(event)
			case <-stop:
				for {
					select {
					case event := <-application.Events():
						printer.print(event)
					default:
						return
					}
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// RunOnce runs a single prompt and streams the answer to a writer.
func RunOnce(application *app.App, prompt string, out io.Writer) error {
	return RunOnceWithContext(context.Background(), application, prompt, out)
}

// RunOnceWithContext is RunOnce under a caller-supplied context.
func RunOnceWithContext(ctx context.Context, application *app.App, prompt string, out io.Writer) error {
	printer := newPlainPrinter(out, isTerminalWriter(out))
	application.SetInteractor(&plainInteractor{in: os.Stdin, out: out})
	stop := drainEvents(application, printer)
	err := application.RunTurn(ctx, ExpandMentions(application.Workspace(), prompt))
	// Stopping first waits for the printer, so the answer is fully written and
	// the recorded failure is safe to read.
	stop()
	if printer.lastLine {
		fmt.Fprintln(out)
	}
	if err != nil {
		return err
	}
	return printer.failure
}

func printPlainWelcome(application *app.App, out io.Writer, colored bool) {
	styles := NewStyles(DefaultPalette())
	banner := RenderBanner(styles)
	if !colored {
		banner = stripANSI(banner)
	}
	fmt.Fprintln(out, banner)
	fmt.Fprintf(out, "%s %s\n", version.Name, version.Version)
	trust := "untrusted"
	if application.Trusted() {
		trust = "trusted"
	}
	fmt.Fprintf(out, "folder: %s (%s)\n", application.Workspace(), trust)
	fmt.Fprintf(out, "model:  %s\n", orNone(application.ModelLabel()))
	fmt.Fprintln(out, "Type a request, or /help for commands. /exit quits.")
}

func plainPrompt(colored bool) string {
	if !colored {
		return "> "
	}
	return ansiCyan + "> " + ansiReset
}

func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none, run /setup)"
	}
	return value
}

// runPlainSlash handles slash commands without a TUI. Read-only commands
// print the same state the TUI shows, so a slash that works on screen also
// works over a pipe. Anything that needs a picker lists instead of opening
// one, because there is no overlay to open.
func runPlainSlash(ctx context.Context, application *app.App, name, args string, out io.Writer) (bool, error) {
	switch name {
	case "exit", "quit":
		return true, nil
	case "help", "?":
		for _, entry := range SlashHelp() {
			parts := strings.SplitN(entry, "\t", 2)
			summary := ""
			if len(parts) > 1 {
				summary = parts[1]
			}
			fmt.Fprintf(out, "  %-16s %s\n", parts[0], summary)
		}
	case "model":
		if strings.TrimSpace(args) == "" {
			fmt.Fprintf(out, "  model: %s (%s)\n", application.ModelLabel(), application.CurrentModel().Provider)
			return false, nil
		}
		model, err := application.SetModelByQuery(args)
		if err != nil {
			return false, err
		}
		fmt.Fprintf(out, "  model is now %s\n", model.Label)
	case "new":
		application.NewSession()
		fmt.Fprintln(out, "  started a new session")
	case "sessions":
		fields := strings.Fields(args)
		if len(fields) == 0 {
			sessions, err := agent.ListSessions()
			if err != nil {
				return false, err
			}
			if len(sessions) == 0 {
				fmt.Fprintln(out, "  no saved sessions yet")
				return false, nil
			}
			fmt.Fprintf(out, "  %d saved session(s):\n", len(sessions))
			for _, session := range sessions {
				title := strings.TrimSpace(session.Title)
				if title == "" {
					title = "(untitled)"
				}
				fmt.Fprintf(out, "    %s  %s  %d turn(s)  %s\n", session.ID, title, session.Turns, session.UpdatedAt.Format("2006-01-02 15:04"))
			}
			return false, nil
		}
		switch strings.ToLower(fields[0]) {
		case "list":
			sessions, err := agent.ListSessions()
			if err != nil {
				return false, err
			}
			fmt.Fprintln(out, formatSessionList(sessions))
			return false, nil
		case "search":
			sessions, err := agent.SearchSessions(strings.TrimSpace(strings.TrimPrefix(args, fields[0])))
			if err != nil {
				return false, err
			}
			fmt.Fprintln(out, formatSessionList(sessions))
			return false, nil
		case "rename":
			if len(fields) < 3 {
				return false, fmt.Errorf("usage: /sessions rename <id> <title>")
			}
			summary, err := agent.RenameSession(fields[1], strings.Join(fields[2:], " "))
			if err != nil {
				return false, err
			}
			fmt.Fprintf(out, "  renamed %s to %q\n", summary.ID, summary.Title)
			return false, nil
		case "delete", "rm":
			if len(fields) < 2 {
				return false, fmt.Errorf("usage: /sessions delete <id>")
			}
			if err := agent.DeleteSession(fields[1]); err != nil {
				return false, err
			}
			if strings.EqualFold(application.Session().ID(), fields[1]) {
				application.NewSession()
			}
			fmt.Fprintf(out, "  deleted %s\n", fields[1])
			return false, nil
		case "export":
			if len(fields) < 2 {
				return false, fmt.Errorf("usage: /sessions export <id> [--format markdown|jsonl]")
			}
			format := agent.SessionExportMarkdown
			for index := 2; index+1 < len(fields); index++ {
				if strings.EqualFold(fields[index], "--format") {
					format = agent.SessionExportFormat(strings.ToLower(fields[index+1]))
					break
				}
			}
			document, err := agent.ExportSession(fields[1], format)
			if err != nil {
				return false, err
			}
			fmt.Fprintln(out, document)
			return false, nil
		default:
			// A bare id resumes nothing in plain mode; point at search.
			sessions, err := agent.SearchSessions(args)
			if err != nil {
				return false, err
			}
			fmt.Fprintln(out, formatSessionList(sessions))
			return false, nil
		}
	case "stop":
		application.Stop()
		fmt.Fprintln(out, "  stopping")
	case "status":
		fmt.Fprintln(out, application.Status())
	case "cost":
		usage := application.Usage()
		howMuch, known := application.Cost()
		unpriced := application.CostUnpriced()
		spend := "cost unknown for this model"
		if known {
			spend = fmt.Sprintf("about $%.4f", howMuch)
			if unpriced > 0 {
				spend += fmt.Sprintf(" (a floor: %d delegated run(s) with no priced model)", unpriced)
			}
		}
		fmt.Fprintf(out, "  tokens: %d in, %d out, %d total\n", usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
		fmt.Fprintf(out, "  estimated spend: %s\n", spend)
		for _, stat := range application.ToolStats() {
			if stat.Errors > 0 {
				fmt.Fprintf(out, "    %-16s %d call(s), %d failed\n", stat.Name, stat.Calls, stat.Errors)
				continue
			}
			fmt.Fprintf(out, "    %-16s %d call(s)\n", stat.Name, stat.Calls)
		}
	case "trust":
		switch strings.ToLower(strings.TrimSpace(args)) {
		case "on", "yes", "true":
			if err := application.SetTrust(true); err != nil {
				return false, err
			}
			fmt.Fprintln(out, "  folder is now trusted")
		case "off", "no", "false":
			if err := application.SetTrust(false); err != nil {
				return false, err
			}
			fmt.Fprintln(out, "  folder is now untrusted")
		case "":
			state := "untrusted"
			if application.Trusted() {
				state = "trusted"
			}
			fmt.Fprintf(out, "  folder is %s\n", state)
			fmt.Fprintln(out, "  change it with /trust on|off")
		default:
			fmt.Fprintf(out, "  folder is %s\n", map[bool]string{true: "trusted", false: "untrusted"}[application.Trusted()])
			return false, fmt.Errorf("usage: /trust [on|off]")
		}
	case "approval":
		if strings.TrimSpace(args) == "" {
			fmt.Fprintf(out, "  approval mode: %s\n", application.Config().ApprovalMode)
			return false, nil
		}
		mode, err := config.ParseApprovalMode(args)
		if err != nil {
			return false, err
		}
		if err := application.SetApprovalMode(mode); err != nil {
			return false, err
		}
		fmt.Fprintf(out, "  approval mode is now %s\n", mode)
	case "harness":
		trimmed := strings.TrimSpace(args)
		if trimmed == "" {
			active := application.HarnessProfile()
			fmt.Fprintf(out, "  harness: %s (%s)\n", active.Label, active.ID)
			for _, profile := range agent.HarnessProfiles() {
				marker := "  "
				if profile.ID == active.ID {
					marker = "> "
				}
				fmt.Fprintf(out, "    %s%-14s %s\n", marker, profile.ID, profile.Description)
			}
			return false, nil
		}
		profile, err := application.SetHarnessProfile(trimmed)
		if err != nil {
			return false, err
		}
		fmt.Fprintf(out, "  harness is now %s\n", profile.Label)
	case "plan":
		items := application.Todos().Items()
		if len(items) == 0 {
			fmt.Fprintln(out, "  plan is empty")
			return false, nil
		}
		done := 0
		for _, item := range items {
			if item.Status == "completed" {
				done++
			}
		}
		fmt.Fprintf(out, "  plan (%d/%d):\n", done, len(items))
		for _, item := range items {
			mark := "[ ]"
			switch item.Status {
			case "in_progress":
				mark = "[>]"
			case "completed":
				mark = "[x]"
			}
			fmt.Fprintf(out, "    %s %s\n", mark, item.Title)
		}
	case "tools":
		entries := application.Tools().Catalog()
		fmt.Fprintf(out, "  tools (%d):\n", len(entries))
		for _, entry := range entries {
			marker := "read"
			if entry.Mutating {
				marker = "write"
			}
			fmt.Fprintf(out, "    %-16s %-6s %s\n", entry.Name, marker, entry.Description)
		}
	case "mcp":
		trimmed := strings.TrimSpace(args)
		if strings.EqualFold(trimmed, "reload") {
			reloadCtx, cancel := context.WithTimeout(ctx, mcpReloadTimeout)
			defer cancel()
			application.ReloadMCP(reloadCtx)
		} else if trimmed != "" {
			return false, fmt.Errorf("usage: /mcp [reload]")
		}
		status := application.MCPStatus()
		if len(status) == 0 {
			fmt.Fprintln(out, "  no MCP servers are configured")
			return false, nil
		}
		fmt.Fprintf(out, "  MCP servers (%d):\n", len(status))
		for _, entry := range status {
			switch {
			case entry.Disabled:
				fmt.Fprintf(out, "    %-14s off       %s\n", entry.Name, entry.Command)
			case entry.Err != nil:
				fmt.Fprintf(out, "    %-14s failed    %s\n", entry.Name, entry.Command)
				fmt.Fprintf(out, "                   %s\n", firstLine(entry.Err.Error()))
			default:
				fmt.Fprintf(out, "    %-14s ready     %d tool(s)  (%s)\n", entry.Name, entry.ToolCount, entry.Command)
			}
		}
	case "skills":
		if strings.EqualFold(strings.TrimSpace(args), "reload") {
			application.ReloadSkills()
		}
		skills := application.Skills()
		if len(skills) == 0 {
			fmt.Fprintln(out, "  no skills found")
			return false, nil
		}
		fmt.Fprintf(out, "  skills (%d):\n", len(skills))
		for _, item := range skills {
			fmt.Fprintf(out, "    %-20s %-8s %s\n", item.Name, item.Scope, item.Description)
		}
	case "memory":
		project, global := agent.NewMemory(application.Workspace()).Read()
		if len(project) == 0 && len(global) == 0 {
			fmt.Fprintln(out, "  nothing learned yet")
			return false, nil
		}
		fmt.Fprintln(out, "  learned memory:")
		for _, fact := range global {
			fmt.Fprintf(out, "    [global] %s\n", fact)
		}
		for _, fact := range project {
			fmt.Fprintf(out, "    [project] %s\n", fact)
		}
	case "telegram":
		sub := strings.ToLower(strings.TrimSpace(args))
		switch sub {
		case "", "status":
			fmt.Fprintf(out, "  telegram: %s\n", application.TelegramStatus())
		case "on":
			if err := application.SetTelegramEnabled(true); err != nil {
				return false, err
			}
			fmt.Fprintln(out, "  telegram bot is running")
		case "off":
			if err := application.SetTelegramEnabled(false); err != nil {
				return false, err
			}
			fmt.Fprintln(out, "  telegram bot stopped")
		case "pair":
			code, err := application.EnsurePairingCode()
			if err != nil {
				return false, err
			}
			_ = application.StartTelegram()
			fmt.Fprintf(out, "  pairing code: %s\n", code)
		case "setup":
			fmt.Fprintln(out, "  run the interactive UI to connect the Telegram companion bot,")
			fmt.Fprintln(out, "  or set the token with: termixgo secret telegram <token>")
		default:
			return false, fmt.Errorf("usage: /telegram [status|on|off|setup|pair]")
		}
	case "init":
		if !application.HasModel() {
			return false, fmt.Errorf("pick a model first with /setup")
		}
		if err := application.RunTurn(ctx, initPrompt); err != nil {
			return false, err
		}
		fmt.Fprintln(out)
	case "worktree":
		fields := strings.Fields(args)
		action := "list"
		if len(fields) > 0 {
			action = fields[0]
		}
		tool, ok := application.Tools().Lookup("git_worktree")
		if !ok {
			return false, fmt.Errorf("the worktree tool is not available")
		}
		callArgs := map[string]any{"action": action}
		if len(fields) > 1 {
			callArgs["path"] = strings.Join(fields[1:], " ")
		}
		if strings.EqualFold(action, "add") && len(fields) > 2 {
			callArgs["path"] = fields[1]
			callArgs["branch"] = strings.Join(fields[2:], " ")
		}
		result, err := tool.Run(ctx, &agent.Env{Workspace: application.Workspace()}, callArgs)
		if err != nil {
			return false, err
		}
		if result.IsError {
			return false, fmt.Errorf("%s", result.Output)
		}
		fmt.Fprintf(out, "  %s\n", result.Output)
		return false, nil
	case "checkpoint":
		trimmed := strings.TrimSpace(args)
		if strings.EqualFold(trimmed, "list") {
			checkpoints, err := agent.ListCheckpoints(ctx, application.Workspace())
			if err != nil {
				return false, err
			}
			if len(checkpoints) == 0 {
				fmt.Fprintln(out, "  no checkpoints yet")
				return false, nil
			}
			fmt.Fprintf(out, "  checkpoints (%d):\n", len(checkpoints))
			for _, item := range checkpoints {
				fmt.Fprintf(out, "    %s  %s\n", item.Ref, item.Message)
			}
			return false, nil
		}
		checkpoint, err := agent.CreateCheckpoint(ctx, application.Workspace(), trimmed)
		if err != nil {
			return false, err
		}
		if checkpoint.Ref == "" {
			fmt.Fprintln(out, "  nothing to save: the working tree is clean")
			return false, nil
		}
		fmt.Fprintf(out, "  checkpoint %s saved\n", checkpoint.Ref)
	case "rewind":
		trimmed := strings.TrimSpace(args)
		var checkpoint agent.Checkpoint
		var err error
		if trimmed == "" {
			checkpoint, err = agent.RewindToLatest(ctx, application.Workspace())
		} else {
			checkpoint, err = agent.RewindToCheckpoint(ctx, application.Workspace(), trimmed)
		}
		if err != nil {
			return false, err
		}
		fmt.Fprintf(out, "  restored %s\n", checkpoint.Ref)
	case "ps":
		manager := application.Processes()
		if manager == nil {
			fmt.Fprintln(out, "  background processes are not available")
			return false, nil
		}
		fields := strings.Fields(args)
		if len(fields) > 0 && strings.EqualFold(fields[0], "kill") {
			if len(fields) < 2 {
				return false, fmt.Errorf("usage: /ps kill <handle>")
			}
			process, err := manager.Kill(fields[1])
			if err != nil {
				return false, err
			}
			if process.Exited() {
				fmt.Fprintf(out, "  %s had already exited with code %d\n", process.ID, process.ExitCode())
			} else {
				fmt.Fprintf(out, "  stopping %s\n", process.ID)
			}
			return false, nil
		}
		processes := manager.List()
		if len(processes) == 0 {
			fmt.Fprintln(out, "  no background processes")
			return false, nil
		}
		fmt.Fprintf(out, "  %d background process(es), stop one with /ps kill <handle>\n", len(processes))
		for _, process := range processes {
			fmt.Fprintf(out, "    %s\n", process.Summary())
		}
	case "setup":
		fmt.Fprintln(out, "  Run the interactive UI (termixgo with no arguments) to use the setup wizard,")
		fmt.Fprintln(out, "  or set a key from the shell: termixgo secret <provider> <key>")
	default:
		item, err := customcmd.Load(application.Workspace(), name)
		if err != nil {
			return false, fmt.Errorf("unknown command /%s. Try /help", name)
		}
		if !application.HasModel() {
			return false, fmt.Errorf("pick a model first with /setup")
		}
		if err := application.RunTurn(ctx, item.Expand(args)); err != nil {
			return false, err
		}
		fmt.Fprintln(out)
	}
	return false, nil
}
