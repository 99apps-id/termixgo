package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Bounds on managed background processes. A dev server is meant to outlive a
// turn, so these exist to stop an agent from spawning work the operator cannot
// see or stop.
const (
	maxBackgroundProcesses = 8
	processBufferBytes     = 256 * 1024
	maxLogChunkChars       = 16000
	// processWaitDelay bounds how long Wait blocks on the output pipe after the
	// direct child exits, which a surviving grandchild would otherwise hold
	// open forever.
	processWaitDelay = 5 * time.Second
	// shutdownWait is how long Shutdown waits for each process to go.
	shutdownWait = 10 * time.Second
)

// ErrNoSuchProcess reports a handle that is not in the manager. It is a
// sentinel so each tool can format its own message and point at the recovery
// path that tool offers.
var ErrNoSuchProcess = errors.New("no such background process")

// Process is one managed background command.
type Process struct {
	ID      string
	Command string
	Dir     string
	Started time.Time

	mu      sync.Mutex
	buffer  *ringBuffer
	cmd     *exec.Cmd
	tree    *processTree
	cancel  context.CancelFunc
	done    chan struct{}
	exited  bool
	exitErr error
}

// stop kills the process and everything it started.
func (p *Process) stop() {
	if p.tree != nil && p.cmd != nil {
		// The tree is what actually reaches children; the cancel below ends
		// the direct child even if the job or group could not be set up.
		_ = p.tree.terminate(p.cmd)
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	if p.cancel != nil {
		p.cancel()
	}
}

// Wait blocks until the process has exited, or the timeout elapses.
func (p *Process) Wait(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		return true
	case <-timer.C:
		return false
	}
}

// Exited reports whether the process has finished.
func (p *Process) Exited() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exited
}

// ExitCode returns the exit status, or -1 while the process is running.
func (p *Process) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitCodeLocked()
}

func (p *Process) exitCodeLocked() int {
	if !p.exited {
		return -1
	}
	var exitErr *exec.ExitError
	if errors.As(p.exitErr, &exitErr) {
		return exitErr.ExitCode()
	}
	if p.exitErr != nil {
		return -1
	}
	return 0
}

// Logs returns output written after an offset, the next offset to pass in, and
// whether any output was lost because the buffer wrapped.
func (p *Process) Logs(offset int64) (text string, next int64, dropped bool) {
	return p.buffer.ReadSince(offset)
}

// LogsAll returns everything the buffer still holds.
func (p *Process) LogsAll() string {
	text, _, _ := p.buffer.ReadSince(0)
	return text
}

// Summary renders one process for a listing.
func (p *Process) Summary() string {
	state := "running"
	if p.Exited() {
		state = fmt.Sprintf("exited(%d)", p.ExitCode())
	}
	age := time.Since(p.Started).Round(time.Second)
	return fmt.Sprintf("%s  %-14s %8s  %s", p.ID, state, age.String(), Shorten(p.Command, 70))
}

// ProcessManager owns the background processes a session started.
//
// It lives on the app rather than in one run's environment, because the whole
// point of a background process is to outlive the turn that started it.
type ProcessManager struct {
	mu        sync.Mutex
	processes map[string]*Process
	order     []string
	nextID    int
	emit      Emitter
}

// NewProcessManager builds an empty manager.
func NewProcessManager() *ProcessManager {
	return &ProcessManager{processes: map[string]*Process{}}
}

// SetEmitter installs the sink for start and exit notices. The app sets it once;
// a process outlives the run that started it, so the sink must too.
func (m *ProcessManager) SetEmitter(emit Emitter) {
	m.mu.Lock()
	m.emit = emit
	m.mu.Unlock()
}

// shellForProcess picks the interpreter for a background command. It is a
// variable rather than a direct call so tests can substitute a cheap shell:
// PowerShell takes seconds to start on some machines, and a test suite should
// measure the manager, not the shell's startup time.
var shellForProcess = shellInvocation

// Start launches a command in the background and returns its handle.
func (m *ProcessManager) Start(ctx context.Context, command, dir string) (*Process, error) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return nil, errors.New("command is required")
	}

	m.mu.Lock()
	if len(m.processes) >= maxBackgroundProcesses {
		m.mu.Unlock()
		return nil, fmt.Errorf("already running %d background processes; stop one with run_kill first", maxBackgroundProcesses)
	}
	m.nextID++
	id := fmt.Sprintf("proc-%d", m.nextID)
	emit := m.emit
	m.mu.Unlock()

	shell, args := shellForProcess(trimmed)
	processCtx, cancel := context.WithCancel(context.Background())
	process := &Process{
		ID:      id,
		Command: trimmed,
		Dir:     dir,
		Started: time.Now(),
		buffer:  newRingBuffer(processBufferBytes),
		done:    make(chan struct{}),
	}
	// The process is detached from the caller's context on purpose: a dev
	// server must survive the turn that started it. It is still tied to the
	// manager, which kills it on shutdown.
	cmd := exec.CommandContext(processCtx, shell, args...)
	cmd.Dir = dir
	cmd.Stdout = process.buffer
	cmd.Stderr = process.buffer
	cmd.Stdin = strings.NewReader("")
	// A grandchild that inherits the output pipe keeps it open after the direct
	// child exits, and Wait would then block on the pipe forever. WaitDelay
	// bounds that wait; the process tree below stops the grandchild.
	cmd.WaitDelay = processWaitDelay

	tree, treeErr := newProcessTree()
	if treeErr == nil {
		tree.prepare(cmd)
		process.tree = tree
	}
	process.cmd = cmd
	process.cancel = cancel

	if err := cmd.Start(); err != nil {
		cancel()
		if tree != nil {
			tree.release()
		}
		return nil, fmt.Errorf("could not start the command: %w", err)
	}
	if tree != nil {
		if err := tree.attach(cmd); err != nil {
			// The command is already running and still useful; only the
			// ability to stop its children is lost, so this is a warning.
			if emit != nil {
				emit(Event{Kind: EventNotice, Text: fmt.Sprintf(
					"Note: %s could not be grouped with its children, so stopping it may leave them running: %v", id, err)})
			}
		}
	}

	m.mu.Lock()
	m.processes[id] = process
	m.order = append(m.order, id)
	m.mu.Unlock()

	if emit != nil {
		emit(Event{Kind: EventNotice, Text: fmt.Sprintf("Started %s in the background: %s", id, Shorten(trimmed, 80))})
	}

	go func() {
		err := cmd.Wait()
		process.mu.Lock()
		process.exited = true
		process.exitErr = err
		process.mu.Unlock()
		close(process.done)
		cancel()

		if emit != nil {
			note := fmt.Sprintf("Background process %s exited with code %d", id, process.ExitCode())
			if process.ExitCode() != 0 {
				// A failed background process is worth seeing without being
				// asked for; that is often the whole reason the operator
				// checked the transcript.
				tail := Shorten(lastLines(process.LogsAll(), 5), 300)
				if tail != "" {
					note += "\n" + tail
				}
			}
			emit(Event{Kind: EventNotice, Text: note})
		}
	}()
	return process, nil
}

// Get finds a process by handle.
func (m *ProcessManager) Get(id string) (*Process, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	process, ok := m.processes[strings.TrimSpace(id)]
	return process, ok
}

// List returns the processes oldest first.
func (m *ProcessManager) List() []*Process {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Process, 0, len(m.order))
	for _, id := range m.order {
		if process, ok := m.processes[id]; ok {
			out = append(out, process)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// Kill terminates one process and everything it started. It is idempotent:
// killing a finished process is not an error, because the operator asking to
// stop something that already stopped has got what they wanted.
func (m *ProcessManager) Kill(id string) (*Process, error) {
	process, ok := m.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNoSuchProcess, strings.TrimSpace(id))
	}
	if process.Exited() {
		return process, nil
	}
	process.stop()
	return process, nil
}

// Shutdown kills every process and waits for them to go.
//
// Waiting matters: without it the caller exits while children still hold their
// working directory and their output pipes, which on Windows makes the next
// cleanup fail and on any platform leaves the process running.
func (m *ProcessManager) Shutdown() {
	processes := m.List()
	if len(processes) == 0 {
		return
	}
	for _, process := range processes {
		if !process.Exited() {
			process.stop()
		}
	}
	for _, process := range processes {
		process.Wait(shutdownWait)
	}
	for _, process := range processes {
		if process.tree != nil {
			// Closing the job is what catches a child that survived the
			// terminate call.
			process.tree.release()
		}
	}
}

// Running counts processes that have not finished.
func (m *ProcessManager) Running() int {
	count := 0
	for _, process := range m.List() {
		if !process.Exited() {
			count++
		}
	}
	return count
}

// ringBuffer keeps the most recent bytes written to it, plus a running total so
// a reader can ask for "everything since offset N" and be told when output was
// lost to the wrap.
type ringBuffer struct {
	mu      sync.Mutex
	data    []byte
	max     int
	written int64
}

func newRingBuffer(max int) *ringBuffer {
	return &ringBuffer{max: max}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.written += int64(len(p))
	r.data = append(r.data, p...)
	if len(r.data) > r.max {
		// Keep the tail: for a failing command the reason is at the end.
		r.data = append([]byte(nil), r.data[len(r.data)-r.max:]...)
	}
	return len(p), nil
}

// ReadSince returns the text after an offset, the new offset, and whether the
// requested offset had already been evicted.
func (r *ringBuffer) ReadSince(offset int64) (string, int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if offset < 0 {
		offset = 0
	}
	firstAvailable := r.written - int64(len(r.data))
	dropped := offset < firstAvailable
	if dropped {
		offset = firstAvailable
	}
	if offset > r.written {
		offset = r.written
	}
	start := int(offset - firstAvailable)
	text := strings.TrimRight(string(r.data[start:]), "\n")
	if len(text) > maxLogChunkChars {
		// Keep the tail and say so, so a huge burst cannot flood the model.
		text = "... [earlier output omitted]\n" + text[len(text)-maxLogChunkChars:]
	}
	return text, r.written, dropped
}

// lastLines returns the final n lines of text.
func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
