package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxPooledSubagents bounds how many subagent sessions the pool keeps. A
// finished one is evicted oldest first; a running one is never evicted.
const maxPooledSubagents = 24

// maxBackgroundSubagents bounds the detached workers at once: each is a full
// model run that outlives the turn, so the per-turn delegation cap does not
// reach them.
const maxBackgroundSubagents = 4

// maxSubagentWait caps one subagent_status wait.
const maxSubagentWait = 10 * time.Minute

// SubagentPool keeps every subagent's session for the life of the process, so
// a finished worker can be continued with send_subagent instead of starting
// over, and a background one can run while the main turn moves on.
type SubagentPool struct {
	mu      sync.Mutex
	entries map[string]*pooledSubagent
	nextID  int
	emit    Emitter
}

type pooledSubagent struct {
	id          string
	kind        string
	description string
	slot        *subagentSlot
	started     time.Time
	running     bool
	background  bool
	answer      string
	err         error
	// spend is a background run's accounting not yet folded into a turn. The
	// turn that reads the result takes it, so delegated work is counted once.
	spend   SubagentSpend
	unspent bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewSubagentPool builds an empty pool.
func NewSubagentPool() *SubagentPool {
	return &SubagentPool{entries: map[string]*pooledSubagent{}}
}

// SetEmitter installs the sink for completion wakes. Like the process
// manager's, it must outlive any one turn.
func (p *SubagentPool) SetEmitter(emit Emitter) {
	p.mu.Lock()
	p.emit = emit
	p.mu.Unlock()
}

// subagentSlot carries one pooled session into RunSubagent through the
// context, so the app's role and model routing stays untouched: the first run
// fills the slot, a continuation finds it filled.
type subagentSlot struct{ session *Session }

type subagentSlotKey struct{}

func withSubagentSlot(ctx context.Context, slot *subagentSlot) context.Context {
	return context.WithValue(ctx, subagentSlotKey{}, slot)
}

func subagentSlotFrom(ctx context.Context) *subagentSlot {
	slot, _ := ctx.Value(subagentSlotKey{}).(*subagentSlot)
	return slot
}

func (p *SubagentPool) create(kind, description string) *pooledSubagent {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	entry := &pooledSubagent{
		id:          fmt.Sprintf("agent-%d", p.nextID),
		kind:        kind,
		description: strings.TrimSpace(description),
		slot:        &subagentSlot{},
		started:     time.Now(),
	}
	p.entries[entry.id] = entry
	p.pruneLocked()
	return entry
}

func (p *SubagentPool) get(id string) (*pooledSubagent, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.entries[strings.TrimSpace(id)]
	return entry, ok
}

// pruneLocked evicts the oldest finished entries past the cap.
func (p *SubagentPool) pruneLocked() {
	if len(p.entries) <= maxPooledSubagents {
		return
	}
	finished := make([]*pooledSubagent, 0, len(p.entries))
	for _, entry := range p.entries {
		if !entry.running {
			finished = append(finished, entry)
		}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].started.Before(finished[j].started) })
	for _, entry := range finished {
		if len(p.entries) <= maxPooledSubagents {
			return
		}
		delete(p.entries, entry.id)
	}
}

// begin marks an entry as running. It refuses an entry already running: one
// session cannot take two turns at once.
func (p *SubagentPool) begin(entry *pooledSubagent, background bool, cancel context.CancelFunc) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if entry.running {
		return fmt.Errorf("subagent %s is still running; wait for it with subagent_status", entry.id)
	}
	if background {
		running := 0
		for _, other := range p.entries {
			if other.running && other.background {
				running++
			}
		}
		if running >= maxBackgroundSubagents {
			return fmt.Errorf("already running %d background subagents; wait for one with subagent_status first", maxBackgroundSubagents)
		}
	}
	entry.running = true
	entry.background = background
	entry.answer = ""
	entry.err = nil
	entry.cancel = cancel
	entry.done = make(chan struct{})
	entry.started = time.Now()
	return nil
}

// finish records a run's outcome. keepSpend is true for a background run,
// whose accounting waits for the turn that reads it.
func (p *SubagentPool) finish(entry *pooledSubagent, answer string, spend SubagentSpend, err error, keepSpend bool) {
	p.mu.Lock()
	entry.running = false
	entry.answer = answer
	entry.err = err
	if keepSpend {
		if entry.unspent {
			entry.spend.Fold(spend)
		} else {
			entry.spend = spend
		}
		entry.unspent = true
	}
	if entry.cancel != nil {
		entry.cancel()
		entry.cancel = nil
	}
	done := entry.done
	emit := p.emit
	background := entry.background
	p.mu.Unlock()
	if done != nil {
		close(done)
	}
	if background && emit != nil {
		state := "finished"
		if err != nil {
			state = "failed"
		}
		emit(Event{Kind: EventWake, ToolName: entry.id, Text: fmt.Sprintf(
			"Background subagent %s (%s) %s. Read its report with subagent_status id=%s.", entry.id, entry.title(), state, entry.id)})
	}
}

// takeSpend hands a background run's accounting to the caller once.
func (p *SubagentPool) takeSpend(entry *pooledSubagent) (SubagentSpend, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !entry.unspent {
		return SubagentSpend{}, false
	}
	spend := entry.spend
	entry.spend = SubagentSpend{}
	entry.unspent = false
	return spend, true
}

// StopAll cancels every running subagent, the shutdown path.
func (p *SubagentPool) StopAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, entry := range p.entries {
		if entry.cancel != nil {
			entry.cancel()
		}
	}
}

func (p *SubagentPool) list() string {
	// Everything a row needs is read under the lock: begin rewrites started and
	// the state while a worker may be starting.
	type row struct {
		id, state, kind, title string
		started                time.Time
	}
	p.mu.Lock()
	rows := make([]row, 0, len(p.entries))
	for _, entry := range p.entries {
		state := "done"
		switch {
		case entry.running:
			state = "running"
		case entry.err != nil:
			state = "failed"
		}
		rows = append(rows, row{entry.id, state, entry.kind, entry.title(), entry.started})
	}
	p.mu.Unlock()
	if len(rows) == 0 {
		return "No subagents in this session."
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].started.Before(rows[j].started) })
	var out strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&out, "%s  %-8s %-12s %s\n", r.id, r.state, r.kind, Shorten(r.title, 60))
	}
	return strings.TrimRight(out.String(), "\n")
}

// startedAt reads when an entry's current run began, under the pool lock.
func (p *SubagentPool) startedAt(entry *pooledSubagent) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return entry.started
}

func (e *pooledSubagent) title() string {
	if e.description != "" {
		return e.description
	}
	return LookupSubagent(e.kind).Label
}

// runPooled runs one pass of a pooled subagent, in the foreground or the
// background. The foreground result carries the spend for the calling turn;
// a background one keeps it for whichever turn reads the report.
func runPooled(ctx context.Context, env *Env, entry *pooledSubagent, prompt string, background bool) (Result, error) {
	pool := env.Subagents
	if !background {
		if err := pool.begin(entry, false, nil); err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		report, spend, err := env.RunSubagent(withSubagentSlot(ctx, entry.slot), entry.kind, prompt)
		pool.finish(entry, report, spend, err, false)
		result := spendResult(spend)
		if err != nil {
			result.Output = subagentFailure(err, report)
			result.IsError = true
			return result, nil
		}
		result.Output = report + "\n\n[subagent " + entry.id + "; continue it with send_subagent]"
		return result, nil
	}
	// A background run is detached from the turn: the turn ends, the worker
	// keeps going, and its completion wakes the model.
	runCtx, cancel := context.WithCancel(context.Background())
	if err := pool.begin(entry, true, cancel); err != nil {
		cancel()
		return Result{Output: err.Error(), IsError: true}, nil
	}
	run := env.RunSubagent
	go func() {
		report, spend, err := run(withSubagentSlot(runCtx, entry.slot), entry.kind, prompt)
		pool.finish(entry, report, spend, err, true)
	}()
	return Result{Output: fmt.Sprintf(
		"Started subagent %s (%s) in the background. You will be notified when it finishes; read the report with subagent_status id=%s, or continue it with send_subagent once it is done.",
		entry.id, entry.title(), entry.id)}, nil
}

func spendResult(spend SubagentSpend) Result {
	return Result{
		SubagentUsage:      spend.Usage,
		SubagentCost:       spend.Cost,
		SubagentCostKnown:  spend.CostKnown,
		SubagentUnpriced:   spend.Unpriced,
		SubagentProvider:   spend.Provider,
		SubagentModel:      spend.Model,
		SubagentEditedCode: spend.EditedCode,
		SubagentSpent:      true,
	}
}

// sendSubagentTool continues a pooled subagent with a follow-up message. The
// worker keeps its whole session, so a follow-up costs only the new turn.
type sendSubagentTool struct{}

func (t *sendSubagentTool) Name() string      { return "send_subagent" }
func (t *sendSubagentTool) Aliases() []string { return []string{"continue_subagent", "send_message"} }
func (t *sendSubagentTool) Mutating() bool    { return false }
func (t *sendSubagentTool) Risk() Risk        { return RiskEdit }
func (t *sendSubagentTool) Label(a map[string]any) string {
	return "Messaging " + argString(a, "id") + ": " + Shorten(argString(a, "message"), 50)
}
func (t *sendSubagentTool) DoneLabel(a map[string]any) string {
	return "Messaged " + argString(a, "id") + ": " + Shorten(argString(a, "message"), 50)
}
func (t *sendSubagentTool) Description() string {
	return "Continue a subagent started by run_subagent with a follow-up message. It keeps its full context from earlier passes. Set background to let it work while you continue."
}
func (t *sendSubagentTool) Schema() map[string]any {
	return object(map[string]any{
		"id":         strProp("The subagent id run_subagent returned, for example agent-1."),
		"message":    strProp("The follow-up instruction or question."),
		"background": boolProp("Run this pass in the background and be notified when it finishes."),
	}, "id", "message")
}

func (t *sendSubagentTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if env.Subagents == nil || env.RunSubagent == nil {
		return Result{Output: "Subagents are not available in this session.", IsError: true}, nil
	}
	message := argString(args, "message", "prompt")
	if strings.TrimSpace(message) == "" {
		return Result{Output: "message is required", IsError: true}, nil
	}
	entry, ok := env.Subagents.get(argString(args, "id"))
	if !ok {
		return Result{Output: "No subagent with that id. List them with subagent_status.", IsError: true}, nil
	}
	return runPooled(ctx, env, entry, message, argBool(args, "background", false))
}

// subagentStatusTool lists pooled subagents, waits for one, reads its report,
// or stops it.
type subagentStatusTool struct{}

func (t *subagentStatusTool) Name() string      { return "subagent_status" }
func (t *subagentStatusTool) Aliases() []string { return []string{"subagent_result", "list_subagents"} }
func (t *subagentStatusTool) Mutating() bool    { return false }
func (t *subagentStatusTool) Risk() Risk        { return RiskEdit }
func (t *subagentStatusTool) Label(a map[string]any) string {
	if id := argString(a, "id"); id != "" {
		return "Checking subagent " + id
	}
	return "Listing subagents"
}
func (t *subagentStatusTool) DoneLabel(a map[string]any) string {
	if id := argString(a, "id"); id != "" {
		return "Checked subagent " + id
	}
	return "Listed subagents"
}
func (t *subagentStatusTool) Description() string {
	return "Without id, list this session's subagents. With id, return that subagent's state and report; wait_seconds waits for a running one, stop cancels it."
}
func (t *subagentStatusTool) Schema() map[string]any {
	return object(map[string]any{
		"id":           strProp("The subagent id. Omit to list all."),
		"wait_seconds": intProp("Wait up to this many seconds for a running subagent to finish (max 600)."),
		"stop":         boolProp("Cancel the running subagent."),
	})
}

func (t *subagentStatusTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	pool := env.Subagents
	if pool == nil {
		return Result{Output: "Subagents are not available in this session.", IsError: true}, nil
	}
	id := argString(args, "id")
	if id == "" {
		return Result{Output: pool.list()}, nil
	}
	entry, ok := pool.get(id)
	if !ok {
		return Result{Output: "No subagent with that id.\n" + pool.list(), IsError: true}, nil
	}
	pool.mu.Lock()
	running, done, cancel := entry.running, entry.done, entry.cancel
	pool.mu.Unlock()
	if running && argBool(args, "stop", false) {
		if cancel == nil {
			return Result{Output: entry.id + " runs in the foreground of another call and cannot be stopped here.", IsError: true}, nil
		}
		cancel()
		<-done
		running = false
	}
	if running {
		wait := time.Duration(argInt(args, "wait_seconds", 0, 0, 600)) * time.Second
		if wait > maxSubagentWait {
			wait = maxSubagentWait
		}
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-done:
				running = false
			case <-timer.C:
			case <-ctx.Done():
			}
			timer.Stop()
		}
	}
	if running {
		return Result{Output: fmt.Sprintf("%s (%s) is still running, %s so far.", entry.id, entry.title(), time.Since(pool.startedAt(entry)).Round(time.Second))}, nil
	}
	result := Result{}
	if spend, ok := pool.takeSpend(entry); ok {
		result = spendResult(spend)
	}
	pool.mu.Lock()
	answer, err := entry.answer, entry.err
	pool.mu.Unlock()
	if err != nil {
		result.Output = fmt.Sprintf("%s (%s) failed: %s", entry.id, entry.title(), subagentFailure(err, answer))
		result.IsError = true
		return result, nil
	}
	if strings.TrimSpace(answer) == "" {
		answer = "The subagent finished without a written answer."
	}
	result.Output = fmt.Sprintf("%s (%s) is done.\n\n%s", entry.id, entry.title(), answer)
	return result, nil
}
