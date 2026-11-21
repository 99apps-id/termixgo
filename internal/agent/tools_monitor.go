package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Monitor pacing. Output is polled, matches are batched so a burst of lines
// becomes one wake, and wakes are spaced so a chatty log cannot flood turns.
var (
	monitorPoll     = 500 * time.Millisecond
	monitorCooldown = 2 * time.Second
)

const (
	monitorMaxLines   = 20
	monitorLineLength = 240
)

// emitter returns the manager's long-lived sink.
func (m *ProcessManager) emitter() Emitter {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emit
}

// monitorTool watches a background process and wakes the model when a line
// of its output matches a pattern, so the model need not poll run_logs while
// a server boots or a long test runs.
type monitorTool struct{}

func (t *monitorTool) Name() string      { return "run_monitor" }
func (t *monitorTool) Aliases() []string { return []string{"monitor", "watch_process"} }
func (t *monitorTool) Mutating() bool    { return false }
func (t *monitorTool) Risk() Risk        { return RiskCommand }
func (t *monitorTool) Label(a map[string]any) string {
	return "Watching " + argString(a, "handle", "id") + " for " + Shorten(argString(a, "pattern"), 40)
}
func (t *monitorTool) DoneLabel(a map[string]any) string {
	return "Watching " + argString(a, "handle", "id") + " for " + Shorten(argString(a, "pattern"), 40)
}
func (t *monitorTool) Description() string {
	return "Watch a background process started by run_background and get notified when a line of its output matches a regular expression (for example ready|listening|FAIL|panic), and when it exits. Returns at once; each notification arrives as a background event, so do not poll run_logs meanwhile."
}
func (t *monitorTool) Schema() map[string]any {
	return object(map[string]any{
		"handle":          strProp("Handle returned by run_background."),
		"pattern":         strProp("Regular expression matched against each output line. Prefix (?i) to ignore case."),
		"max_events":      intProp("Stop after this many notifications, 1 to 20. Defaults to 3."),
		"timeout_minutes": intProp("Stop watching after this many minutes, 1 to 240. Defaults to 60."),
		"from_start":      boolProp("Also scan output written before the monitor started."),
		"on_exit":         boolProp("Notify when the process exits. Defaults to true."),
	}, "handle", "pattern")
}

// maxMonitors bounds the watchers running at once. Each polls its process every
// half second for up to four hours, so an unbounded number is a slow leak.
const maxMonitors = 8

// acquireMonitor reserves a watcher slot, reporting false when all are taken.
func (m *ProcessManager) acquireMonitor() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.monitors >= maxMonitors {
		return false
	}
	m.monitors++
	return true
}

func (m *ProcessManager) releaseMonitor() {
	m.mu.Lock()
	if m.monitors > 0 {
		m.monitors--
	}
	m.mu.Unlock()
}

func (t *monitorTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if env.NoWake {
		// A chat turn has no way to receive the notification: it would land in
		// the terminal's event stream, in a conversation the chat never sees.
		return Result{Output: "Monitoring is not available in this conversation; use run_wait or run_logs to check the process.", IsError: true}, nil
	}
	if env.Processes == nil {
		return Result{Output: "Background processes are not available in this session.", IsError: true}, nil
	}
	id := argString(args, "handle", "id")
	process, ok := env.Processes.Get(id)
	if !ok {
		return Result{Output: unknownHandleMessage(id), IsError: true}, nil
	}
	raw := argString(args, "pattern")
	if strings.TrimSpace(raw) == "" {
		return Result{Output: "pattern is required", IsError: true}, nil
	}
	pattern, err := regexp.Compile(raw)
	if err != nil {
		return Result{Output: "invalid pattern: " + err.Error(), IsError: true}, nil
	}
	emit := env.Processes.emitter()
	if emit == nil {
		return Result{Output: "Monitoring needs a front end that can receive notifications.", IsError: true}, nil
	}
	watch := processWatch{
		process:   process,
		pattern:   pattern,
		maxEvents: argInt(args, "max_events", 3, 1, 20),
		deadline:  time.Now().Add(time.Duration(argInt(args, "timeout_minutes", 60, 1, 240)) * time.Minute),
		onExit:    argBool(args, "on_exit", true),
		emit:      emit,
	}
	if !argBool(args, "from_start", false) {
		_, watch.offset, _ = process.Logs(0)
	}
	if process.Exited() && !argBool(args, "from_start", false) {
		return Result{Output: fmt.Sprintf("%s has already exited with code %d; read its output with run_logs.", id, process.ExitCode()), IsError: true}, nil
	}
	if !env.Processes.acquireMonitor() {
		return Result{Output: fmt.Sprintf("Already watching %d processes; wait for one to finish its notifications first.", maxMonitors), IsError: true}, nil
	}
	go func() {
		defer env.Processes.releaseMonitor()
		watch.run()
	}()
	return Result{Output: fmt.Sprintf(
		"Watching %s for /%s/ (up to %d notifications). You will be notified as matches arrive; carry on with other work or end your turn.",
		id, raw, watch.maxEvents)}, nil
}

type processWatch struct {
	process   *Process
	pattern   *regexp.Regexp
	offset    int64
	maxEvents int
	deadline  time.Time
	onExit    bool
	emit      Emitter
}

func (w *processWatch) run() {
	ticker := time.NewTicker(monitorPoll)
	defer ticker.Stop()
	var (
		carry    string
		pending  []string
		events   int
		lastWake time.Time
	)
	for {
		exited := false
		select {
		case <-w.process.done:
			exited = true
		case <-ticker.C:
		}
		text, next, _ := w.process.Logs(w.offset)
		w.offset = next
		lines := strings.Split(carry+text, "\n")
		// The last piece is a line still being written; it is matched once it
		// is complete, or at exit when nothing more will come.
		carry = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
		if exited && carry != "" {
			lines = append(lines, carry)
			carry = ""
		}
		for _, line := range lines {
			line = strings.TrimRight(line, "\r")
			if w.pattern.MatchString(line) && len(pending) < monitorMaxLines {
				pending = append(pending, Shorten(strings.TrimSpace(line), monitorLineLength))
			}
		}
		if len(pending) > 0 && (exited || time.Since(lastWake) >= monitorCooldown) {
			events++
			lastWake = time.Now()
			w.wake(fmt.Sprintf("Monitor on %s matched /%s/:\n%s", w.process.ID, w.pattern.String(), strings.Join(pending, "\n")))
			pending = nil
			if events >= w.maxEvents {
				return
			}
		}
		if exited {
			if w.onExit {
				w.wake(fmt.Sprintf("%s exited with code %d.\n%s", w.process.ID, w.process.ExitCode(), lastLines(w.process.LogsAll(), 5)))
			}
			return
		}
		if time.Now().After(w.deadline) {
			return
		}
	}
}

func (w *processWatch) wake(text string) {
	w.emit(Event{Kind: EventWake, ToolName: w.process.ID, Text: strings.TrimSpace(text)})
}
