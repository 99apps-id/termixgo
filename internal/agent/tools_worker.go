package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// workerDepthEnv marks a native worker child, so it cannot start another worker
// and recurse.
const workerDepthEnv = "TERMIXGO_WORKER_DEPTH"

// codeWorkerArgv builds the command line for a coding worker. It is a variable
// so a test can substitute a trivial process instead of a real CLI.
var codeWorkerArgv = buildWorkerArgv

func buildWorkerArgv(kind, task string) ([]string, error) {
	if strings.TrimSpace(task) == "" {
		return nil, fmt.Errorf("the task is empty")
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "claude":
		return []string{"claude", "--print", "--permission-mode", "bypassPermissions", task}, nil
	case "codex":
		return []string{"codex", "exec", task}, nil
	case "opencode":
		return []string{"opencode", "run", task}, nil
	case "termixgo":
		// The native worker needs no external account: it runs this same
		// binary against the provider already configured here.
		executable, err := os.Executable()
		if err != nil || strings.TrimSpace(executable) == "" {
			executable = "termixgo"
		}
		return []string{executable, "run", task}, nil
	default:
		return nil, fmt.Errorf("unknown worker %q; use termixgo, claude, codex or opencode", kind)
	}
}

// codeWorkerTool hands a coding task to an external coding agent in its own
// worktree. The worker runs as a detached process, so it survives the turn, and
// its completion is announced in chat.
//
// It is not a second in-process turn: the run loop is single-turn by design, so
// a worker has to be a process the manager owns.
type codeWorkerTool struct{}

func (t *codeWorkerTool) Name() string      { return "code_worker" }
func (t *codeWorkerTool) Aliases() []string { return []string{"coding_worker", "delegate_coding"} }
func (t *codeWorkerTool) Mutating() bool    { return true }
func (t *codeWorkerTool) Risk() Risk        { return RiskCommand }
func (t *codeWorkerTool) Label(a map[string]any) string {
	return "Starting " + workerKind(a) + " coding worker"
}
func (t *codeWorkerTool) DoneLabel(a map[string]any) string {
	return "Started " + workerKind(a) + " coding worker"
}
func (t *codeWorkerTool) Description() string {
	return "Hand a self-contained coding task to a background coding agent in its own git worktree. The worker can be termixgo (this same binary, which needs no external account and uses the configured model) or claude, codex, opencode. It runs detached and survives the turn; its completion is announced in chat. Use it for a long task that would otherwise block the conversation."
}
func (t *codeWorkerTool) Schema() map[string]any {
	return object(map[string]any{
		"task":   strProp("The coding task, written as a complete instruction."),
		"worker": strProp("Which coding agent: termixgo (this same binary, no external account), claude (default), codex or opencode."),
		"name":   strProp("Optional worktree name; a short one is generated when omitted."),
	}, "task")
}

func (t *codeWorkerTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if os.Getenv(workerDepthEnv) != "" {
		return Result{Output: "a background worker cannot start another worker", IsError: true}, nil
	}
	task := strings.TrimSpace(argString(args, "task"))
	if task == "" {
		return Result{Output: "task is required", IsError: true}, nil
	}
	if env.Processes == nil {
		return Result{Output: "background processes are not available in this session", IsError: true}, nil
	}
	kind := workerKind(args)
	argv, err := codeWorkerArgv(kind, task)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return Result{Output: fmt.Sprintf("the worker %q is not installed or not on PATH", argv[0]), IsError: true}, nil
	}

	name := strings.TrimSpace(argString(args, "name"))
	if name == "" {
		name = "worker-" + shortToken()
	}
	// One worktree per task, so a worker never edits the main tree.
	if created, err := worktreeAdd(ctx, env, map[string]any{"path": name, "branch": "termixgo/" + name}); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	} else if created.IsError {
		return created, nil
	}
	dir := worktreePath(env, name)
	var childEnv []string
	if kind == "termixgo" {
		childEnv = []string{workerDepthEnv + "=1"}
	}
	process, err := env.Processes.StartWorkerEnv(ctx, dir, argv, kind+" worker "+name, true, childEnv)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Started %s worker %s in %s. It runs in the background; you will be told here when it finishes.", kind, process.ID, dir)}, nil
}

func workerKind(args map[string]any) string {
	kind := strings.ToLower(strings.TrimSpace(argString(args, "worker")))
	if kind == "" {
		return "claude"
	}
	return kind
}

// shortToken is a short random suffix for a generated worktree name.
func shortToken() string {
	buffer := make([]byte, 3)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
	}
	return hex.EncodeToString(buffer)
}
