package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

// hookBlockExit is the exit status a hook uses to say "stop": a pre_tool hook
// blocks the call, and a post_tool hook hands its message back to the model.
// Any other non-zero status is the hook itself being broken, which is reported
// to the operator and never mistaken for a policy decision.
const hookBlockExit = 2

// Bounds on what a hook may put in front of the model or the terminal.
const (
	hookMessageMax = 4000
	hookPayloadMax = 8 * 1024
	hookCaptureMax = 64 * 1024
)

// hookPayload is the JSON a hook reads on stdin. Arguments travel here and
// nowhere else: they are model-authored, so they must never be spliced into the
// command line.
type hookPayload struct {
	Event     string          `json:"event"`
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args"`
	Workspace string          `json:"workspace"`
	Output    string          `json:"output,omitempty"`
	IsError   *bool           `json:"isError,omitempty"`
}

// hookOutcome is what one hook run decided.
type hookOutcome struct {
	// blocked is set by exit status 2.
	blocked bool
	// message is the hook's own words: stderr, then stdout.
	message string
	// warning explains a hook that failed to run or ran out of time. The run
	// continues, so a broken hook costs a notice instead of the session.
	warning string
}

// hooksFor returns the configured hooks for an event whose matcher accepts the
// tool, in file order.
func (r *Runner) hooksFor(event, tool string) []config.Hook {
	var matched []config.Hook
	for _, hook := range r.Config.Hooks {
		if hook.Event != event {
			continue
		}
		if hook.Matcher != "" {
			pattern, err := regexp.Compile(hook.Matcher)
			if err != nil || !pattern.MatchString(tool) {
				continue
			}
		}
		matched = append(matched, hook)
	}
	return matched
}

// runPreHooks runs the pre_tool hooks for a call and reports the first block.
// Hooks run in order and the first one that blocks wins, so a later hook never
// runs on a call that is already refused.
func (r *Runner) runPreHooks(ctx context.Context, tool, rawArgs string) (string, bool) {
	hooks := r.hooksFor(config.HookPreTool, tool)
	if len(hooks) == 0 {
		return "", false
	}
	payload := r.hookPayload(config.HookPreTool, tool, rawArgs, nil)
	for _, hook := range hooks {
		outcome := runHookCommand(ctx, hook, r.hookDir(), payload)
		r.noteHookWarning(hook, outcome)
		if outcome.blocked {
			return outcome.message, true
		}
	}
	return "", false
}

// runPostHooks runs the post_tool hooks and joins whatever they asked the model
// to hear. A hook that only has side effects, a formatter for instance, stays
// silent and adds nothing to the transcript.
func (r *Runner) runPostHooks(ctx context.Context, tool, rawArgs string, result Result) string {
	hooks := r.hooksFor(config.HookPostTool, tool)
	if len(hooks) == 0 || ctx.Err() != nil {
		return ""
	}
	failed := result.IsError
	payload := r.hookPayload(config.HookPostTool, tool, rawArgs, &hookResultView{output: result.Output, isError: failed})
	var feedback []string
	for _, hook := range hooks {
		outcome := runHookCommand(ctx, hook, r.hookDir(), payload)
		r.noteHookWarning(hook, outcome)
		if outcome.blocked && outcome.message != "" {
			feedback = append(feedback, outcome.message)
		}
	}
	return strings.Join(feedback, "\n")
}

// hookResultView carries a finished tool call into the post_tool payload.
type hookResultView struct {
	output  string
	isError bool
}

func (r *Runner) hookDir() string {
	if r.Env == nil {
		return ""
	}
	return r.Env.Workspace
}

func (r *Runner) hookPayload(event, tool, rawArgs string, result *hookResultView) []byte {
	args := strings.TrimSpace(rawArgs)
	if args == "" || !json.Valid([]byte(args)) {
		args = "{}"
	}
	payload := hookPayload{
		Event:     event,
		Tool:      tool,
		Args:      json.RawMessage(args),
		Workspace: r.hookDir(),
	}
	if result != nil {
		payload.Output = clipBytes(result.output, hookPayloadMax)
		isError := result.isError
		payload.IsError = &isError
	}
	// json.Marshal of these field types cannot fail, and an empty payload would
	// leave the hook with nothing to read, so fall back to a bare object. The
	// line ends in a newline because line-oriented tools (sh read, findstr)
	// treat an unterminated last line as no line at all.
	data, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}\n")
	}
	return append(data, '\n')
}

func (r *Runner) noteHookWarning(hook config.Hook, outcome hookOutcome) {
	if outcome.warning == "" {
		return
	}
	r.emit(Event{Kind: EventNotice, Text: fmt.Sprintf("Hook %s (%s) not enforced: %s", hook.Event, clipBytes(hook.Command, 60), outcome.warning)})
}

// runHookCommand runs one hook and classifies how it ended.
func runHookCommand(ctx context.Context, hook config.Hook, dir string, payload []byte) hookOutcome {
	timeout := time.Duration(hook.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = config.DefaultHookTimeoutSecs * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell, shellArgs := shellForTool(hook.Command)
	process := exec.CommandContext(runCtx, shell, shellArgs...)
	process.Dir = dir
	// The hook is the operator's own command, so it keeps the operator's
	// environment: a reporting hook legitimately needs its tokens. Only the
	// model's input is untrusted here, and that rides on stdin.
	process.Env = os.Environ()
	var stdout, stderr capWriter
	stdout.limit, stderr.limit = hookCaptureMax, hookCaptureMax
	process.Stdout = &stdout
	process.Stderr = &stderr
	process.Stdin = bytes.NewReader(payload)

	tree, treeErr := newProcessTree()
	if treeErr == nil {
		tree.prepare(process)
		process.Cancel = func() error {
			if err := tree.terminate(process); err == nil {
				return nil
			}
			if process.Process != nil {
				return process.Process.Kill()
			}
			return nil
		}
	}
	process.WaitDelay = processWaitDelay

	if err := process.Start(); err != nil {
		if tree != nil {
			tree.release()
		}
		return hookOutcome{warning: fmt.Sprintf("could not start: %v", err)}
	}
	if tree != nil {
		_ = tree.attach(process)
		defer tree.release()
	}

	runErr := process.Wait()
	if runCtx.Err() == context.DeadlineExceeded {
		return hookOutcome{warning: fmt.Sprintf("timed out after %s", timeout)}
	}
	if ctx.Err() != nil {
		return hookOutcome{}
	}
	if runErr == nil {
		return hookOutcome{}
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		return hookOutcome{warning: fmt.Sprintf("could not run: %v", runErr)}
	}
	message := hookMessage(stderr.String(), stdout.String())
	if exitErr.ExitCode() == hookBlockExit {
		if message == "" {
			message = "no reason given"
		}
		return hookOutcome{blocked: true, message: message}
	}
	detail := fmt.Sprintf("exited with status %d", exitErr.ExitCode())
	if message != "" {
		detail += ": " + message
	}
	return hookOutcome{warning: detail}
}

// hookMessage prefers stderr, where a hook writes its reason by convention, and
// falls back to stdout. It is clipped on a rune boundary because it goes both to
// the model and to the terminal.
func hookMessage(stderr, stdout string) string {
	text := strings.TrimSpace(stderr)
	if text == "" {
		text = strings.TrimSpace(stdout)
	}
	return clipBytes(text, hookMessageMax)
}

// capWriter keeps the first limit bytes and discards the rest, so a hook that
// prints without end cannot grow the process. It still reports every write as
// accepted, because a short write would make the child fail on a full pipe.
type capWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if room := w.limit - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

func (w *capWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
