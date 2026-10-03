package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ParallelWorker describes one task in a parallel batch.
type ParallelWorker struct {
	Task   string
	Worker string
	Name   string
}

// parallelBatchTool starts several coding workers at once, each in its own
// worktree. One turn can only run one thing, so a batch of independent tasks
// fans out to detached workers that run in parallel and announce completion
// in chat when each finishes.
type parallelBatchTool struct{}

func (t *parallelBatchTool) Name() string      { return "parallel_batch" }
func (t *parallelBatchTool) Aliases() []string { return []string{"parallel_workers", "fan_out"} }
func (t *parallelBatchTool) Mutating() bool    { return true }
func (t *parallelBatchTool) Risk() Risk        { return RiskCommand }
func (t *parallelBatchTool) Label(a map[string]any) string {
	tasks := parseParallelBatch(a)
	return fmt.Sprintf("Starting %d parallel workers%s", len(tasks), batchKinds(tasks))
}
func (t *parallelBatchTool) DoneLabel(a map[string]any) string {
	tasks := parseParallelBatch(a)
	return fmt.Sprintf("Started %d parallel workers%s", len(tasks), batchKinds(tasks))
}
func (t *parallelBatchTool) Description() string {
	return "Start several background coding agents at once, each in its own git worktree. Give tasks as a list of {task, worker, name}. Each worker runs detached and announces completion in chat. Use it when independent tasks can run in parallel without touching each other's files."
}
func (t *parallelBatchTool) Schema() map[string]any {
	return object(map[string]any{
		"tasks":  arrayProp("The tasks to run in parallel, each {task, worker?, name?}.", object(map[string]any{"task": strProp("One self-contained instruction."), "worker": strProp("termixgo, claude, codex or opencode."), "name": strProp("Worktree name.")})),
		"worker": strProp("Default worker for every task: termixgo, claude, codex or opencode."),
		"prefix": strProp("Name prefix for generated worktrees."),
	}, "tasks")
}

// maxParallelBatch bounds one fan-out so a single call cannot flood the
// process table or the disk with checkouts.
const maxParallelBatch = 8

func (t *parallelBatchTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if os.Getenv(workerDepthEnv) != "" {
		return Result{Output: "a background worker cannot start another worker", IsError: true}, nil
	}
	if env.Processes == nil {
		return Result{Output: "background processes are not available in this session", IsError: true}, nil
	}
	tasks := parseParallelBatch(args)
	if len(tasks) == 0 {
		return Result{Output: "tasks must contain at least one {task}", IsError: true}, nil
	}
	if len(tasks) > maxParallelBatch {
		return Result{Output: fmt.Sprintf("at most %d workers per batch; split the rest into a second call", maxParallelBatch), IsError: true}, nil
	}
	prefix := strings.TrimSpace(argString(args, "prefix"))
	if prefix == "" {
		prefix = "worker"
	}

	// Validate every task before creating anything: a batch that fails halfway
	// leaves half the checkouts behind and the operator guessing which half. The
	// worker binary is checked here too, because naming a missing binary before a
	// worktree exists is the same promise the single worker makes.
	argvs := make([][]string, len(tasks))
	names := map[string]bool{}
	for index, task := range tasks {
		if strings.TrimSpace(task.Task) == "" {
			return Result{Output: fmt.Sprintf("task %d is empty", index+1), IsError: true}, nil
		}
		if name := strings.TrimSpace(task.Name); name != "" {
			if names[name] {
				return Result{Output: fmt.Sprintf("task %d reuses the worktree name %q; each worker needs its own checkout", index+1, name), IsError: true}, nil
			}
			names[name] = true
		}
		argv, err := resolveWorkerCommand(env.Config, workerOrDefault(task.Worker), task.Task)
		if err != nil {
			return Result{Output: fmt.Sprintf("task %d: %v", index+1, err), IsError: true}, nil
		}
		if len(argv) == 0 {
			return Result{Output: fmt.Sprintf("task %d: the worker command is empty", index+1), IsError: true}, nil
		}
		if _, err := exec.LookPath(argv[0]); err != nil {
			return Result{Output: fmt.Sprintf("task %d: the worker %q is not installed or not on PATH", index+1, argv[0]), IsError: true}, nil
		}
		argvs[index] = argv
	}

	type started struct {
		name string
		dir  string
		id   string
	}
	results := make([]started, len(tasks))
	errs := make([]error, len(tasks))
	// Serialise the worktree creation: recordWorktree does read-modify-write
	// on one JSON file, so two goroutines racing it lose one entry. Workers
	// still run in parallel; only the checkout setup is sequential.
	var setupMu sync.Mutex
	var wg sync.WaitGroup
	for index, task := range tasks {
		wg.Add(1)
		go func(index int, task ParallelWorker) {
			defer wg.Done()
			name := strings.TrimSpace(task.Name)
			if name == "" {
				name = fmt.Sprintf("%s-%s", prefix, shortToken())
			}
			worker := workerOrDefault(task.Worker)
			argv := argvs[index]
			setupMu.Lock()
			created, workErr := worktreeAdd(ctx, env, map[string]any{"path": name, "branch": "termixgo/" + name})
			setupMu.Unlock()
			if workErr != nil {
				errs[index] = workErr
				return
			} else if created.IsError {
				errs[index] = fmt.Errorf("%s", created.Output)
				return
			}
			dir := worktreePath(env, name)
			var childEnv []string
			if worker == "termixgo" {
				childEnv = []string{workerDepthEnv + "=1"}
			}
			process, err := env.Processes.StartWorkerEnv(ctx, dir, argv, worker+" worker "+name, true, childEnv)
			if err != nil {
				errs[index] = err
				return
			}
			results[index] = started{name: name, dir: dir, id: process.ID}
		}(index, task)
	}
	wg.Wait()

	var lines []string
	failed := 0
	for index, task := range tasks {
		if errs[index] != nil {
			failed++
			lines = append(lines, fmt.Sprintf("  %d. %s: FAILED: %v", index+1, shortenTask(task.Task), errs[index]))
			continue
		}
		lines = append(lines, fmt.Sprintf("  %d. %s worker %s in %s (%s)", index+1, workerOrDefault(task.Worker), results[index].id, results[index].name, results[index].dir))
	}
	summary := fmt.Sprintf("Started %d of %d parallel workers:", len(tasks)-failed, len(tasks))
	if failed > 0 {
		summary = fmt.Sprintf("Started %d of %d parallel workers (%d failed):", len(tasks)-failed, len(tasks), failed)
	}
	lines = append([]string{summary}, lines...)
	lines = append(lines, "Each runs in the background; completions are announced here.")
	if failed > 0 {
		return Result{Output: strings.Join(lines, "\n"), IsError: len(tasks)-failed == 0}, nil
	}
	return Result{Output: strings.Join(lines, "\n")}, nil
}

// sameWorktree matches a process to its checkout by path, not by substring. Two
// names where one is a prefix of the other, "alpha" and "alpha-2", would
// otherwise share whichever worker was listed first, so one checkout read as
// running while the other sat idle.
func sameWorktree(process *Process, entry worktreeEntry) bool {
	if process == nil {
		return false
	}
	if strings.TrimSpace(entry.Path) != "" && filepath.Clean(process.Dir) == filepath.Clean(entry.Path) {
		return true
	}
	return strings.TrimSpace(entry.Name) != "" && strings.HasSuffix(process.Label, " worker "+entry.Name)
}

// batchKinds names the distinct workers in a batch, so the approval prompt
// shows which agents are being started instead of only how many. One approval
// fans out to that many detached workers, each with its own checkout.
func batchKinds(tasks []ParallelWorker) string {
	var kinds []string
	seen := map[string]bool{}
	for _, task := range tasks {
		kind := workerOrDefault(task.Worker)
		if seen[kind] {
			continue
		}
		seen[kind] = true
		kinds = append(kinds, kind)
	}
	if len(kinds) == 0 {
		return ""
	}
	return " (" + strings.Join(kinds, ", ") + ")"
}

// workerOrDefault normalises an empty worker to the default.
func workerOrDefault(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "claude"
	}
	return strings.ToLower(strings.TrimSpace(kind))
}

// shortenTask keeps the batch summary to one line per worker.
func shortenTask(task string) string {
	one := strings.Join(strings.Fields(task), " ")
	return Shorten(one, 60)
}

// parseParallelBatch reads tasks as [{task, worker?, name?}].
func parseParallelBatch(args map[string]any) []ParallelWorker {
	raw, ok := args["tasks"].([]any)
	if !ok {
		return nil
	}
	out := make([]ParallelWorker, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		task := strings.TrimSpace(argString(entry, "task"))
		if task == "" {
			continue
		}
		out = append(out, ParallelWorker{
			Task:   task,
			Worker: strings.TrimSpace(argString(entry, "worker", "kind")),
			Name:   strings.TrimSpace(argString(entry, "name")),
		})
	}
	// A shared default fills blanks, so a batch on one worker does not repeat it.
	def := strings.TrimSpace(argString(args, "worker", "kind"))
	for index := range out {
		if out[index].Worker == "" {
			out[index].Worker = def
		}
	}
	return out
}

// BatchStatusFor renders batchStatus for callers outside the agent package.
func BatchStatusFor(workspace string, manager *ProcessManager, now time.Time) string {
	return batchStatus(workspace, manager, now)
}

// batchStatus renders one line per tracked worktree plus its worker state,
// so the operator sees which parallel checkout is done without hunting logs.
func batchStatus(workspace string, manager *ProcessManager, now time.Time) string {
	entries, err := loadWorktreeEntries(workspace)
	if err != nil || len(entries) == 0 {
		return "No tracked worktrees."
	}
	var lines []string
	for _, entry := range entries {
		state := "idle " + shortAge(now.Sub(entry.LastUsedAt))
		if manager != nil {
			for _, process := range manager.List() {
				if !sameWorktree(process, entry) {
					continue
				}
				if process.Exited() {
					if process.ExitCode() == 0 {
						state = "done"
					} else {
						state = fmt.Sprintf("failed (exit %d)", process.ExitCode())
					}
				} else {
					state = "running " + process.ID
				}
				break
			}
		}
		lines = append(lines, fmt.Sprintf("  %-20s %-16s %s", entry.Name, state, entry.Path))
	}
	return fmt.Sprintf("Parallel worktrees (%d):\n%s", len(lines), strings.Join(lines, "\n"))
}
