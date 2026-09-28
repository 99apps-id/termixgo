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
}

func (i *plainInteractor) Approve(request agent.ApprovalRequest) agent.Decision {
	fmt.Fprintf(i.out, "\nApproval needed: %s (%s)\n  %s\n[y] once  [s] session  [a] always  [n] deny: ",
		request.Tool, request.Risk, request.Detail)
	reader := bufio.NewReader(i.in)
	line, err := reader.ReadString('\n')
	if err != nil {
		return agent.DecisionDeny
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
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
	reader := bufio.NewReader(i.in)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	answer := strings.TrimSpace(line)
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
		if name, args, ok := ParseSlash(line); ok {
			quit, err := runPlainSlash(application, name, args, out)
			if err != nil {
				fmt.Fprintln(out, printer.color(ansiRed, "error: "+err.Error()))
			}
			if quit {
				break
			}
			continue
		}
		if err := application.RunTurn(ctx, line); err != nil {
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
	err := application.RunTurn(ctx, prompt)
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

// runPlainSlash handles the subset of commands that make sense without a TUI.
func runPlainSlash(application *app.App, name, args string, out io.Writer) (bool, error) {
	switch name {
	case "exit", "quit":
		return true, nil
	case "help":
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
	case "stop":
		application.Stop()
		fmt.Fprintln(out, "  stopping")
	case "status":
		fmt.Fprintln(out, application.Status())
	case "cost":
		usage := application.Usage()
		howMuch, known := application.Cost()
		spend := "cost unknown for this model"
		if known {
			spend = fmt.Sprintf("about $%.4f", howMuch)
		}
		fmt.Fprintf(out, "  tokens: %d in, %d out, %d total\n", usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
		fmt.Fprintf(out, "  estimated spend: %s\n", spend)
	case "trust":
		switch strings.ToLower(strings.TrimSpace(args)) {
		case "on", "yes":
			if err := application.SetTrust(true); err != nil {
				return false, err
			}
			fmt.Fprintln(out, "  folder is now trusted")
		case "off", "no":
			if err := application.SetTrust(false); err != nil {
				return false, err
			}
			fmt.Fprintln(out, "  folder is now untrusted")
		default:
			state := "untrusted"
			if application.Trusted() {
				state = "trusted"
			}
			fmt.Fprintf(out, "  folder is %s\n", state)
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
		fmt.Fprintf(out, "  /%s is not available in plain mode. Try /help.\n", name)
	}
	return false, nil
}
