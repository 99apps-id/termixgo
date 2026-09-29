package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

// toolTimeoutCap bounds a single tool call so a hung tool cannot wedge a run.
const toolTimeoutCap = 15 * time.Minute

// SteerPrefix marks an operator message that arrived while the turn was
// already running, so the model can tell a course correction from the original
// request.
const SteerPrefix = "[Operator steer]"

// Runner drives one conversation: it streams from the provider, executes the
// tools the model requests, and reports every step through Env.Emit.
//
// There is no sandbox: every tool in the registry is on the allowlist and
// runs directly on the operator machine. Folder trust is the gate that keeps
// that safe, so a mutating tool in an untrusted folder always asks first.
type Runner struct {
	Client   provider.Client
	Model    string
	Config   config.Config
	Env      *Env
	Tools    *Registry
	Policy   *ApprovalPolicy
	MaxSteps int
	// Harness selects the agent harness profile. Empty means critical.
	Harness string
	// ContextBudget overrides the token budget for history compaction.
	ContextBudget int
	// Pricing is the model's cost per million tokens. Costs are reported
	// through every usage event so the UI can show spend as it accrues.
	Pricing provider.Pricing
	// CostKnown says whether Pricing is a real figure. It is set separately
	// because a free local model has a real price of zero, which is not the
	// same as an unpriced model.
	CostKnown bool
	// CostBudgetUSD stops the run once estimated session spend passes it.
	// Zero means no limit.
	CostBudgetUSD float64
	// System, when set, replaces the assembled system prompt.
	System string
	// Journal records recurring tool failures so the loop can learn from them.
	Journal *ErrorJournal
	// Steer, when set, returns the operator messages typed while this turn is
	// running. The loop folds them into the conversation at the next step
	// boundary, which is what lets the operator change course without
	// aborting the turn. Nil means steering is unavailable.
	Steer func() []string
}

// Run executes one operator turn to completion.
func (r *Runner) Run(ctx context.Context, session *Session, input string) error {
	emit := r.emit
	profile := GetHarnessProfile(r.Harness)
	maxSteps := ApplyHarnessToBudget(r.MaxSteps, profile)
	// The profile reorders and hides tools. Applying it here, once, keeps the
	// schemas the model sees and the registry the calls are resolved against in
	// agreement.
	r.Tools = RegistryForProfile(r.Tools, profile)
	ledger := NewVerifyLedger()
	guard := &loopGuard{}
	nudges := 0

	session.AddUser(input + "\n\n" + FormatEnvironmentBlock(r.Env.Workspace, TrustLabel(r.Env.Trusted)))

	emit(Event{Kind: EventTurnStart})
	stopReason := "stop"
	// Spend is carried across the whole turn, so a chatty loop cannot creep
	// past a budget one small step at a time.
	sessionCost := session.Cost()
	costKnown := r.CostKnown || r.Pricing.Known()

	for step := 0; step < maxSteps; step++ {
		if ctx.Err() != nil {
			stopReason = "aborted"
			break
		}
		if overBudget(r.CostBudgetUSD, sessionCost) {
			stopReason = "cost-cap"
			emit(Event{
				Kind:    EventError,
				Err:     fmt.Errorf("stopped: estimated spend reached $%.4f of the $%.2f budget (config costBudgetUsd)", sessionCost, r.CostBudgetUSD),
				CostUSD: sessionCost,
			},
			)
			break
		}

		// A steer typed while the turn is running is new information, so it also
		// clears the repetition guard: the operator has a reason for the model to
		// try again, and the turn should not close just because it was looping.
		if step > 0 && r.Steer != nil && r.injectSteering(session) {
			*guard = loopGuard{}
		}

		request := provider.ChatRequest{
			Model:    r.Model,
			System:   r.system(session),
			Messages: compactForModel(session.Messages(), r.ContextBudget),
			Tools:    r.Tools.Definitions(),
		}

		var answer strings.Builder
		var reasoning strings.Builder
		var calls []provider.ToolCall
		var turnUsage provider.Usage
		var reasoningStart time.Time

		streamErr := r.Client.Stream(ctx, request, func(event provider.StreamEvent) error {
			switch event.Type {
			case provider.EventTextDelta:
				answer.WriteString(event.Text)
				emit(Event{Kind: EventText, Text: event.Text})
			case provider.EventReasoningDelta:
				if reasoning.Len() == 0 {
					reasoningStart = time.Now()
				}
				reasoning.WriteString(event.Text)
				emit(Event{Kind: EventThinking, Text: event.Text})
			case provider.EventToolCall:
				if event.ToolCall != nil {
					calls = append(calls, *event.ToolCall)
				}
			case provider.EventUsage:
				if event.Usage != nil {
					turnUsage = turnUsage.Add(*event.Usage)
					sessionCost += r.Pricing.Cost(*event.Usage)
					emit(Event{Kind: EventUsage, Usage: *event.Usage, CostUSD: sessionCost, CostKnown: costKnown})
				}
			}
			return nil
		})

		if reasoning.Len() > 0 {
			millis := int64(0)
			if !reasoningStart.IsZero() {
				millis = durationMillis(time.Since(reasoningStart))
			}
			emit(Event{Kind: EventReasoned, Text: reasoning.String(), ToolMillis: millis})
		}

		if streamErr != nil {
			if ctx.Err() != nil {
				stopReason = "aborted"
				break
			}
			emit(Event{Kind: EventError, Err: streamErr})
			stopReason = "error"
			break
		}

		session.AddAssistant(answer.String(), reasoning.String(), calls)

		if len(calls) == 0 {
			if strings.TrimSpace(answer.String()) == "" {
				// Silence is not success: a model that returns no text
				// and no calls is stuck, so ask again up to the guard
				// instead of filing an empty turn as done.
				if stop, reason := guard.noteEmptyStep(); stop {
					stopReason = "loop-guard"
					emit(Event{Kind: EventNotice, Text: reason})
					break
				}
				if ledger.ShouldNudgeVerification(false) && nudges < MaxVerifyNudges {
					nudge := ledger.BuildVerifyNudge(nudges, false)
					if strings.TrimSpace(nudge) != "" {
						nudges++
						session.AddUser(nudge)
						emit(Event{Kind: EventNotice, Text: "Verifying the change before finishing."})
						continue
					}
				}
				continue
			}
			guard.noteProgress()
			if ledger.ShouldNudgeVerification(ClaimsVerification(answer.String())) && nudges < MaxVerifyNudges {
				nudge := ledger.BuildVerifyNudge(nudges, ClaimsVerification(answer.String()))
				if strings.TrimSpace(nudge) != "" {
					nudges++
					session.AddUser(nudge)
					emit(Event{Kind: EventNotice, Text: "Verifying the change before finishing."})
					continue
				}
			}
			session.AddUsage(turnUsage)
			session.SetCost(sessionCost)
			emit(Event{Kind: EventTurnEnd, StopReason: "stop", Usage: turnUsage, CostUSD: sessionCost, CostKnown: costKnown})
			return nil
		}
		guard.noteProgress()

		// A task that moves to completed is the boundary verification belongs to:
		// the next task would otherwise build on a change nobody checked.
		pendingVerify := ""
		for index, call := range calls {
			if ctx.Err() != nil {
				stopReason = "aborted"
				answerSkippedCalls(session, calls[index:], "the turn was stopped")
				break
			}
			if stop, reason := guard.noteCall(call.Name, call.Arguments); stop {
				stopReason = "loop-guard"
				emit(Event{Kind: EventNotice, Text: reason})
				answerSkippedCalls(session, calls[index:], reason)
				break
			}
			before := r.todoSnapshot()
			result := r.execute(ctx, call)
			r.observeToolResult(&ledger, guard, call.Name, call.Arguments, result)
			if result.IsError && r.Journal != nil {
				r.Journal.Record(call.Name, call.Arguments, result.Output)
			}
			session.AddToolResult(call.ID, call.Name, result.Output)
			if !result.IsError && taskJustCompleted(before, r.todoSnapshot()) {
				pendingVerify = ledger.BuildVerifyNudge(nudges, false)
			}
			if stop, reason := guard.noteResult(result.IsError); stop {
				stopReason = "loop-guard"
				emit(Event{Kind: EventNotice, Text: reason})
				answerSkippedCalls(session, calls[index+1:], reason)
				break
			}
			if ctx.Err() != nil {
				stopReason = "aborted"
				answerSkippedCalls(session, calls[index+1:], "the turn was stopped")
				break
			}
		}
		if stopReason == "loop-guard" || stopReason == "aborted" {
			break
		}
		if ctx.Err() != nil {
			break
		}
		if strings.TrimSpace(pendingVerify) != "" && nudges < MaxVerifyNudges {
			nudges++
			session.AddUser(pendingVerify)
			emit(Event{Kind: EventNotice, Text: "Task finished. Verifying before the next one."})
			continue
		}
	}

	if stopReason == "stop" {
		// Only reached when the step budget was exhausted without a stop.
		stopReason = "step-cap"
		emit(Event{Kind: EventNotice, Text: fmt.Sprintf(
			"Paused after %d steps: one turn is capped so a single reply cannot spend forever. "+
				"Nothing is lost, the work so far stays in the transcript. "+
				"Send \"continue\" to pick up exactly where it stopped, or set a larger maxSteps in the settings file "+
				"(the /harness autonomous profile also grants a longer turn).", maxSteps)})
	}
	if stopReason == "aborted" {
		emit(Event{Kind: EventNotice, Text: "Stopped."})
	}
	session.SetCost(sessionCost)
	emit(Event{Kind: EventTurnEnd, StopReason: stopReason, CostUSD: sessionCost, CostKnown: costKnown})
	return nil
}

// overBudget reports whether estimated spend has passed a limit. A limit of
// zero or less means unlimited, and a negative spend cannot happen.
func overBudget(budget, spend float64) bool {
	return budget > 0 && spend >= budget
}

// injectSteering folds the operator messages typed during this turn into the
// conversation. It reports whether anything was applied.
func (r *Runner) injectSteering(session *Session) bool {
	applied := false
	for _, message := range r.Steer() {
		trimmed := strings.TrimSpace(message)
		if trimmed == "" {
			continue
		}
		session.AddUser(SteerPrefix + " " + trimmed)
		r.emit(Event{Kind: EventNotice, Text: "Steering: " + trimmed})
		applied = true
	}
	return applied
}

// todoSnapshot reads the plan before a tool call, so the loop can tell when a
// task moved to completed.
func (r *Runner) todoSnapshot() []Todo {
	if r.Env == nil || r.Env.Todos == nil {
		return nil
	}
	return r.Env.Todos.Items()
}

// taskJustCompleted reports whether an item that was in progress is now done.
func taskJustCompleted(before, after []Todo) bool {
	inProgress := map[string]bool{}
	for _, item := range before {
		if item.Status == "in_progress" && item.ID != "" {
			inProgress[item.ID] = true
		}
	}
	if len(inProgress) == 0 {
		return false
	}
	for _, item := range after {
		if item.Status == "completed" && inProgress[item.ID] {
			return true
		}
	}
	return false
}

// answerSkippedCalls gives every tool call in a batch a result when the loop
// stops part way through it. A provider rejects a request whose assistant
// message carries tool calls with no matching results, so a guard that trips
// mid-batch has to close the batch or the next turn fails for a reason that
// has nothing to do with the operator's work.
func answerSkippedCalls(session *Session, calls []provider.ToolCall, reason string) {
	for _, call := range calls {
		session.AddToolResult(call.ID, call.Name, "Not run: "+reason)
	}
}

// system assembles the system prompt for this step.
func (r *Runner) system(session *Session) string {
	if strings.TrimSpace(r.System) != "" {
		return r.System
	}
	return ApplyHarnessToSystem(BuildSystem(r.Env, r.Model), GetHarnessProfile(r.Harness))
}

// observeToolResult folds one finished tool call into the verify ledger.
func (r *Runner) observeToolResult(ledger *VerifyLedger, guard *loopGuard, name, rawArgs string, result Result) {
	lowered := strings.ToLower(strings.TrimSpace(name))
	switch lowered {
	case "edit", "replace", "multi_edit", "multi_replace", "write_file", "write", "create_file":
		if result.IsError {
			return
		}
		args, err := decodeToolArguments(rawArgs)
		if err != nil {
			return
		}
		path := argString(args, "path", "file", "filename")
		if path == "" && (lowered == "multi_edit" || lowered == "multi_replace") {
			path = argString(args, "path", "file")
		}
		*ledger = ledger.RecordEdit(path)
	case "run_checks", "verify", "test", "lint":
		if !result.IsError {
			*ledger = ledger.RecordVerification()
		}
	case "run_command", "bash", "bash_run", "shell", "exec":
		if result.IsError {
			return
		}
		args, err := decodeToolArguments(rawArgs)
		if err != nil {
			return
		}
		if LooksLikeCheckCommand(argString(args, "command")) {
			*ledger = ledger.RecordVerification()
		}
	}
}

// needsApprovalFor reports whether a call must wait. Trust is the outer
// gate: a mutating tool in an untrusted folder always asks, even when the
// approval mode would allow it. In a trusted folder the allowlist runs.
func (r *Runner) needsApprovalFor(tool Tool) bool {
	if r.Policy != nil && r.Policy.NeedsApproval(tool) {
		return true
	}
	if tool.Mutating() && r.Env != nil && !r.Env.Trusted {
		return true
	}
	return false
}

// execute runs one tool call, applying the approval policy first.
func (r *Runner) execute(ctx context.Context, call provider.ToolCall) Result {
	args, err := decodeToolArguments(call.Arguments)
	if err != nil {
		return Result{Output: fmt.Sprintf("Could not read the arguments for %s: %v", call.Name, err), IsError: true}
	}

	tool, ok := r.Tools.Lookup(call.Name)
	if !ok {
		return Result{
			Output:  fmt.Sprintf("There is no tool named %q. Available tools: %s", call.Name, r.toolNames()),
			IsError: true,
		}
	}

	startLabel := tool.Label(args)
	emit := r.emit
	emit(Event{Kind: EventToolStart, ToolName: tool.Name(), ToolLabel: startLabel, ToolArgs: Shorten(call.Arguments, 240)})

	// Plan mode blocks mutating tools outright instead of asking. Prompting
	// on every write would defeat the mode: the operator chose it to explore
	// without changing anything, so the denial names the way back.
	if r.Policy != nil && r.Policy.Mode == ApprovalPlan && tool.Mutating() {
		emit(Event{Kind: EventToolEnd, ToolName: tool.Name(), ToolLabel: tool.DoneLabel(args), ToolResult: "blocked by plan mode", ToolOK: false})
		return Result{
			Output:  "Plan mode is on: read-only investigation is allowed, but this mutating tool call was blocked. Switch back with /approval ask to allow changes.",
			IsError: true,
		}
	}

	if r.needsApprovalFor(tool) {
		decision := DecisionDeny
		if r.Env.Approve != nil {
			decision = r.Env.Approve(ApprovalRequest{
				Tool:   tool.Name(),
				Detail: startLabel,
				Risk:   string(tool.Risk()),
				Diff:   PreviewToolDiff(r.Env, tool.Name(), args),
			})
		}
		if !decision.Allowed() {
			emit(Event{Kind: EventToolEnd, ToolName: tool.Name(), ToolLabel: tool.DoneLabel(args), ToolResult: "denied by the operator", ToolOK: false})
			return Result{
				Output:  "The operator denied this tool call. Ask before retrying or choose a different approach.",
				IsError: true,
			}
		}
		if decision == DecisionAllowSession {
			r.Policy.AllowSession(tool.Name())
		}
	}

	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, toolTimeoutCap)
	result, runErr := tool.Run(runCtx, r.Env, args)
	cancel()
	elapsed := durationMillis(time.Since(started))

	if runErr != nil {
		result = Result{Output: fmt.Sprintf("%s failed: %v", tool.Name(), runErr), IsError: true}
	}
	display := result.Display
	if strings.TrimSpace(display) == "" {
		display = Shorten(result.Output, 240)
	}
	emit(Event{
		Kind:       EventToolEnd,
		ToolName:   tool.Name(),
		ToolLabel:  tool.DoneLabel(args),
		ToolResult: display,
		ToolOK:     !result.IsError,
		ToolMillis: elapsed,
	})
	if len(result.Plan) > 0 {
		if err := r.Env.Todos.Write(result.Plan); err == nil {
			emit(Event{Kind: EventPlan, Plan: r.Env.Todos.Items()})
		}
	}
	if result.IsError {
		return result
	}
	if strings.TrimSpace(result.Output) == "" {
		result.Output = "(no output)"
	}
	return result
}

func (r *Runner) toolNames() string {
	names := make([]string, 0, len(r.Tools.Tools()))
	for _, tool := range r.Tools.Tools() {
		names = append(names, tool.Name())
	}
	return strings.Join(names, ", ")
}

func (r *Runner) emit(event Event) {
	if r.Env != nil && r.Env.Emit != nil {
		r.Env.Emit(event)
	}
}

// decodeToolArguments parses a tool call's argument object.
func decodeToolArguments(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil, fmt.Errorf("arguments are not a JSON object")
	}
	if decoded == nil {
		decoded = map[string]any{}
	}
	return decoded, nil
}
