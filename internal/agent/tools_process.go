package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// backgroundTool starts a long-running command.
type backgroundTool struct{}

func (t *backgroundTool) Name() string { return "run_background" }
func (t *backgroundTool) Aliases() []string {
	return []string{"bash_background", "spawn", "start_server"}
}
func (t *backgroundTool) Mutating() bool { return true }
func (t *backgroundTool) Risk() Risk     { return RiskCommand }
func (t *backgroundTool) Label(a map[string]any) string {
	return "Starting " + Shorten(argString(a, "command"), 60)
}
func (t *backgroundTool) DoneLabel(a map[string]any) string {
	return "Started " + Shorten(argString(a, "command"), 60)
}
func (t *backgroundTool) Description() string {
	return "Start a long-running command in the background and get a handle: a dev server, a watcher, a test run that takes minutes. Read its output with run_logs, wait for it with run_wait, stop it with run_kill. The process keeps running after this turn ends. On Windows stopping a process may not stop what it started."
}
func (t *backgroundTool) Schema() map[string]any {
	return object(map[string]any{
		"command": strProp("Command line to run in the background."),
		"cwd":     strProp("Working directory. Defaults to the workspace root."),
		"wait_secs": intProp("How long to wait for early output before returning, 1 to 60. " +
			"Defaults to 2. Raise it for a server so a startup crash is visible immediately."),
	}, "command")
}

func (t *backgroundTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if env.Processes == nil {
		return Result{Output: "Background processes are not available in this session.", IsError: true}, nil
	}
	command := strings.TrimSpace(argString(args, "command"))
	if command == "" {
		return Result{Output: "command is required", IsError: true}, nil
	}
	dir := resolvePath(env, argString(args, "cwd"))
	if dir == "" {
		dir = env.Workspace
	}
	process, err := env.Processes.Start(ctx, command, dir)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}

	// A short grace period turns the common failure ("the command does not
	// exist", "the port is taken") into an immediate answer instead of a
	// handle that looks healthy and then dies unnoticed.
	wait := argInt(args, "wait_secs", 2, 1, 60)
	select {
	case <-process.done:
	case <-time.After(time.Duration(wait) * time.Second):
	case <-ctx.Done():
	}

	if process.Exited() {
		output := process.LogsAll()
		code := process.ExitCode()
		message := fmt.Sprintf("The command exited immediately with code %d.\n%s", code, output)
		if code == 0 {
			// A short command that succeeds is a fine outcome, just not a
			// background one.
			return Result{Output: message}, nil
		}
		return Result{Output: message, IsError: true}, nil
	}

	output := process.LogsAll()
	var builder strings.Builder
	fmt.Fprintf(&builder, "Started %s in %s (pid %d).", process.ID, displayPath(env, dir), pidOf(process))
	builder.WriteString("\nUse run_logs to read more, run_wait to wait for exit, run_kill to stop it.")
	if strings.TrimSpace(output) != "" {
		builder.WriteString("\n\nStartup output:\n")
		builder.WriteString(Shorten(output, 2000))
	}
	return Result{Output: builder.String()}, nil
}

// logsTool reads a background process's output.
type logsTool struct{}

func (t *logsTool) Name() string      { return "run_logs" }
func (t *logsTool) Aliases() []string { return []string{"bash_logs", "read_logs"} }
func (t *logsTool) Mutating() bool    { return false }
func (t *logsTool) Risk() Risk        { return RiskCommand }
func (t *logsTool) Label(a map[string]any) string {
	return "Reading logs " + argString(a, "handle", "id")
}
func (t *logsTool) DoneLabel(a map[string]any) string {
	return "Read logs " + argString(a, "handle", "id")
}
func (t *logsTool) Description() string {
	return "Read new output from a background process. Pass the offset from the previous call to get only what is new."
}
func (t *logsTool) Schema() map[string]any {
	return object(map[string]any{
		"handle":       strProp("Handle returned by run_background."),
		"since_offset": intProp("Only return output after this offset. Omit on the first read."),
	}, "handle")
}

func (t *logsTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if env.Processes == nil {
		return Result{Output: "Background processes are not available in this session.", IsError: true}, nil
	}
	id := argString(args, "handle", "id")
	process, ok := env.Processes.Get(id)
	if !ok {
		return Result{Output: unknownHandleMessage(id), IsError: true}, nil
	}
	offset := int64(argInt(args, "since_offset", 0, 0, 0))
	text, next, dropped := process.Logs(offset)

	var builder strings.Builder
	if dropped {
		builder.WriteString("[earlier output was evicted from the buffer]\n")
	}
	if strings.TrimSpace(text) == "" {
		builder.WriteString("(no new output)")
	} else {
		builder.WriteString(text)
	}
	builder.WriteString("\n\n")
	if process.Exited() {
		fmt.Fprintf(&builder, "Process exited with code %d. next_offset=%d", process.ExitCode(), next)
	} else {
		fmt.Fprintf(&builder, "Process still running. next_offset=%d", next)
	}
	return Result{Output: builder.String()}, nil
}

// waitTool waits for a background process to finish.
type waitTool struct{}

func (t *waitTool) Name() string      { return "run_wait" }
func (t *waitTool) Aliases() []string { return []string{"bash_wait"} }
func (t *waitTool) Mutating() bool    { return false }
func (t *waitTool) Risk() Risk        { return RiskCommand }
func (t *waitTool) Label(a map[string]any) string {
	return "Waiting for " + argString(a, "handle", "id")
}
func (t *waitTool) DoneLabel(a map[string]any) string {
	return "Waited for " + argString(a, "handle", "id")
}
func (t *waitTool) Description() string {
	return "Wait for a background process to exit, up to a timeout, and return its exit code with the tail of its output. Use it after starting a long test or build so you can report the real result."
}
func (t *waitTool) Schema() map[string]any {
	return object(map[string]any{
		"handle":       strProp("Handle returned by run_background."),
		"timeout_secs": intProp("How long to wait, 1 to 900. Defaults to 120."),
	}, "handle")
}

func (t *waitTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if env.Processes == nil {
		return Result{Output: "Background processes are not available in this session.", IsError: true}, nil
	}
	id := argString(args, "handle", "id")
	process, ok := env.Processes.Get(id)
	if !ok {
		return Result{Output: unknownHandleMessage(id), IsError: true}, nil
	}
	timeout := argInt(args, "timeout_secs", 120, 1, 900)

	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()
	select {
	case <-process.done:
	case <-timer.C:
		return Result{Output: fmt.Sprintf(
			"%s is still running after %ds. Read progress with run_logs, or stop it with run_kill.", id, timeout)}, nil
	case <-ctx.Done():
		return Result{Output: "Wait cancelled.", IsError: true}, nil
	}

	code := process.ExitCode()
	tail := lastLines(process.LogsAll(), 20)
	message := fmt.Sprintf("%s exited with code %d.\n%s", id, code, tail)
	if code != 0 {
		return Result{Output: message, IsError: true}, nil
	}
	return Result{Output: message}, nil
}

// listProcessesTool lists the background processes.
type listProcessesTool struct{}

func (t *listProcessesTool) Name() string      { return "run_list" }
func (t *listProcessesTool) Aliases() []string { return []string{"bash_list", "ps"} }
func (t *listProcessesTool) Mutating() bool    { return false }
func (t *listProcessesTool) Risk() Risk        { return RiskCommand }
func (t *listProcessesTool) Label(a map[string]any) string {
	return "Listing background processes"
}
func (t *listProcessesTool) DoneLabel(a map[string]any) string {
	return "Listed background processes"
}
func (t *listProcessesTool) Description() string {
	return "List the background processes this session started, with their handle, state and age."
}
func (t *listProcessesTool) Schema() map[string]any { return object(map[string]any{}) }

func (t *listProcessesTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if env.Processes == nil {
		return Result{Output: "Background processes are not available in this session.", IsError: true}, nil
	}
	processes := env.Processes.List()
	if len(processes) == 0 {
		return Result{Output: "No background processes."}, nil
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "%d background process(es):\n", len(processes))
	for _, process := range processes {
		builder.WriteString(process.Summary() + "\n")
	}
	return Result{Output: strings.TrimRight(builder.String(), "\n")}, nil
}

// killTool stops a background process.
type killTool struct{}

func (t *killTool) Name() string      { return "run_kill" }
func (t *killTool) Aliases() []string { return []string{"bash_kill", "stop_process"} }
func (t *killTool) Mutating() bool    { return true }
func (t *killTool) Risk() Risk        { return RiskCommand }
func (t *killTool) Label(a map[string]any) string {
	return "Stopping " + argString(a, "handle", "id")
}
func (t *killTool) DoneLabel(a map[string]any) string {
	return "Stopped " + argString(a, "handle", "id")
}
func (t *killTool) Description() string {
	return "Stop a background process. Safe to call on a process that has already exited. On Windows this may not stop processes the command itself started, so check with run_list."
}
func (t *killTool) Schema() map[string]any {
	return object(map[string]any{
		"handle": strProp("Handle returned by run_background."),
	}, "handle")
}

func (t *killTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if env.Processes == nil {
		return Result{Output: "Background processes are not available in this session.", IsError: true}, nil
	}
	id := argString(args, "handle", "id")
	process, err := env.Processes.Kill(id)
	if errors.Is(err, ErrNoSuchProcess) {
		// The model needs the same recovery hint the other handle tools give,
		// or it retries the same wrong handle.
		return Result{Output: unknownHandleMessage(id), IsError: true}, nil
	}
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if process.Exited() {
		return Result{Output: fmt.Sprintf("%s had already exited with code %d.", id, process.ExitCode())}, nil
	}
	return Result{Output: fmt.Sprintf("Stopping %s. Verify with run_list.", id)}, nil
}

// unknownHandleMessage is shared by the three tools that take a handle, so a
// model that mistypes one always learns how to list what exists.
func unknownHandleMessage(id string) string {
	return fmt.Sprintf("No background process named %q. Use run_list to see them.", id)
}

// pidOf reports the OS process id, or 0 when it is unknown.
func pidOf(process *Process) int {
	if process.cmd == nil || process.cmd.Process == nil {
		return 0
	}
	return process.cmd.Process.Pid
}
