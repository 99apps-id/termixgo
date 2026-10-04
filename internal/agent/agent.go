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
	Client provider.Client
	Model  string
	Config config.Config
	Env    *Env
	Tools  *Registry
	Policy *ApprovalPolicy
	// MaxSteps is the number of steps one turn may run before it pauses. It is
	// the operator's whole per-turn ceiling, not a segment of a larger one.
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
	// ToolSearch loads the ecosystem tools on demand instead of sending every
	// schema on every step. The core loop and find_tools stay visible; a tool
	// the model discovers stays visible for the rest of the turn.
	ToolSearch bool
	// TurnImages are attached to the turn's first user message, which is how an
	// uploaded image reaches a vision model.
	TurnImages []provider.Image
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

	// Tool search installs the discovery hooks on a per-turn copy of the
	// environment, so the search index and the discovered set cannot leak into
	// another turn or a subagent.
	discovered := map[string]bool{}
	if r.ToolSearch && r.Env != nil {
		cloned := *r.Env
		cloned.ToolIndex = buildToolIndex(r.Tools.Tools(), toolSearchAlwaysOn)
		cloned.DiscoverTools = func(names []string) {
			for _, name := range names {
				discovered[strings.ToLower(name)] = true
			}
		}
		r.Env = &cloned
	}

	ledger := NewVerifyLedger()
	guard := &loopGuard{}
	nudges := 0

	turnText := input + "\n\n" + FormatEnvironmentBlock(r.Env.Workspace, TrustLabel(r.Env.Trusted))
	if len(r.TurnImages) > 0 {
		session.AddUserWithImages(turnText, r.TurnImages)
	} else {
		session.AddUser(turnText)
	}

	emit(Event{Kind: EventTurnStart})
	stopReason := "stop"
	// Spend is carried across the whole turn, so a chatty loop cannot creep
	// past a budget one small step at a time.
	sessionCost := session.Cost()
	costKnown := r.CostKnown || r.Pricing.Known()

	// One turn runs up to maxSteps steps, and that number is the operator's
	// maxSteps: the ceiling is explicit rather than a hidden multiple of the
	// setting. The loop guard and the cost cap stay the stops for a run that
	// goes nowhere; this is only the bound that keeps one reply from spending
	// forever.
	//
	// Usage is summed across every step, so the turn total reported at the end
	// is the whole turn rather than its last step.
	var turnUsage provider.Usage
runLoop:
	for step := 0; step < maxSteps; step++ {
		if ctx.Err() != nil {
			stopReason = "aborted"
			break runLoop
		}
		if overBudget(r.CostBudgetUSD, sessionCost) {
			stopReason = "cost-cap"
			emit(Event{
				Kind:    EventError,
				Err:     fmt.Errorf("stopped: estimated spend reached $%.4f of the $%.2f budget (config costBudgetUsd)", sessionCost, r.CostBudgetUSD),
				CostUSD: sessionCost,
			},
			)
			break runLoop
		}

		// A steer typed while the turn is running is new information, so it also
		// clears the repetition guard: the operator has a reason for the model to
		// try again, and the turn should not close just because it was looping.
		if step > 0 && r.Steer != nil && r.injectSteering(session) {
			*guard = loopGuard{}
		}

		request := provider.ChatRequest{
			Model:       r.Model,
			System:      r.system(session),
			SystemParts: r.systemParts(session),
			Messages:    compactForModel(session.Messages(), r.ContextBudget),
			Tools:       r.stepTools(discovered).Definitions(),
		}

		var answer strings.Builder
		var reasoning strings.Builder
		var calls []provider.ToolCall
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
				break runLoop
			}
			// Record the provider failure next to the tool failures so the
			// journal, and the operator, can see why a turn ended without an
			// answer instead of only seeing the tool history.
			if r.Journal != nil {
				r.Journal.Record("provider", "", streamErr.Error())
			}
			emit(Event{Kind: EventError, Err: streamErr})
			stopReason = "error"
			break runLoop
		}

		// A step that produced no text and no tool call is not a turn worth
		// keeping. Storing it would put an empty assistant message, or two
		// in a row when the model is idle, into the history: a weaker
		// OpenAI-compatible model reads that as a malformed turn and can
		// answer an earlier message instead of the operator's latest one.
		// The guard below still counts the idle step, so the loop stops.
		if strings.TrimSpace(answer.String()) != "" || len(calls) > 0 {
			session.AddAssistant(answer.String(), reasoning.String(), calls)
		}

		if len(calls) == 0 {
			if strings.TrimSpace(answer.String()) == "" {
				// Silence is not success: a model that returns no text
				// and no calls is stuck, so ask again up to the guard
				// instead of filing an empty turn as done.
				if stop, reason := guard.noteEmptyStep(); stop {
					stopReason = "loop-guard"
					emit(Event{Kind: EventNotice, Text: reason})
					break runLoop
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
			// The emitter is the single accumulator: App.emit folds every
			// EventUsage into the app total and the session together. Adding
			// the turn total here as well recorded the last step's tokens a
			// second time, so a one step turn persisted twice its usage and
			// the session file disagreed with the status line.
			session.SetCost(sessionCost)
			emit(Event{Kind: EventTurnEnd, StopReason: "stop", Usage: turnUsage, CostUSD: sessionCost, CostKnown: costKnown})
			return nil
		}
		guard.noteProgress()

		// A task that moves to completed is the boundary verification belongs to:
		// the next task would otherwise build on a change nobody checked.
		pendingVerify := ""
		var pendingImages []provider.Image
		diagnose := ""
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
			if result.IsError && ctx.Err() != nil {
				result.Output = "stopped: " + ctx.Err().Error()
			}
			r.observeToolResult(&ledger, guard, call.Name, call.Arguments, result)
			if result.IsError && r.Journal != nil {
				r.Journal.Record(call.Name, call.Arguments, result.Output)
			}
			session.AddToolResult(call.ID, call.Name, result.Output)
			if len(result.Images) > 0 {
				pendingImages = append(pendingImages, result.Images...)
			}
			if result.SubagentSpent {
				// A delegated run folded its tokens and dollars in here, on the
				// parent's own accumulator, so the turn total, /cost, the
				// session ledger and the cost budget all include the child's
				// spend rather than losing it with the throwaway session.
				//
				// Only a delegated run that actually spent tokens can pull the
				// combined figure toward unknown: a subagent that failed before
				// running returns zero tokens, and zero tokens should not cast
				// doubt on the parent's own priced cost.
				if result.SubagentUsage.TotalTokens > 0 {
					costKnown = costKnown && result.SubagentCostKnown
				}
				turnUsage = turnUsage.Add(result.SubagentUsage)
				sessionCost += result.SubagentCost
				// The child's own usage events were dropped inside RunSubagent
				// (they would have streamed its transcript), so this one event
				// is their only route into the app total, the session and the
				// audit ledger.
				emit(Event{Kind: EventUsage, Usage: result.SubagentUsage, CostUSD: sessionCost, CostKnown: costKnown})
			}
			if !result.IsError && taskJustCompleted(before, r.todoSnapshot()) {
				pendingVerify = ledger.BuildVerifyNudge(nudges, false)
			}
			if nudge, reason := guard.noteResult(result.IsError); nudge {
				// A streak of errors is not a loop: keep the run alive, tell the
				// model to diagnose, and let it try a different approach. The
				// remaining calls in the batch still run.
				diagnose = reason
				emit(Event{Kind: EventNotice, Text: reason})
			}
			if ctx.Err() != nil {
				stopReason = "aborted"
				answerSkippedCalls(session, calls[index+1:], "the turn was stopped")
				break
			}
		}
		// Images from a tool in this batch go on one user message after all
		// the tool results, so every provider keeps the results adjacent to
		// the assistant call that requested them.
		if len(pendingImages) > 0 {
			session.AddImages("Image data read by the tools above is attached for you to see.", pendingImages)
		}
		if stopReason == "loop-guard" || stopReason == "aborted" {
			break runLoop
		}
		if ctx.Err() != nil {
			break runLoop
		}
		if diagnose != "" {
			session.AddUser(diagnose)
			continue
		}
		if strings.TrimSpace(pendingVerify) != "" && nudges < MaxVerifyNudges {
			nudges++
			session.AddUser(pendingVerify)
			emit(Event{Kind: EventNotice, Text: "Task finished. Verifying before the next one."})
			continue
		}
	}

	if stopReason == "stop" {
		// Only reached when the step ceiling was hit while work was continuing.
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
	// Usage rides along on every stop path, not just the clean one. The audit
	// ledger reads it off this event, so omitting it here recorded a turn that
	// ran thirty steps as a thirty-step turn that spent nothing.
	session.SetCost(sessionCost)
	emit(Event{Kind: EventTurnEnd, StopReason: stopReason, Usage: turnUsage, CostUSD: sessionCost, CostKnown: costKnown})
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
	prompt := ApplyHarnessToSystem(BuildSystem(r.Env, r.Model), GetHarnessProfile(r.Harness))
	if r.ToolSearch {
		prompt += "\n\n" + toolSearchHint
	}
	return prompt
}

// systemParts returns the static prompt and dynamic working plan separately
// so provider implementations can mark the static prefix for prompt caching.
func (r *Runner) systemParts(session *Session) []string {
	if strings.TrimSpace(r.System) != "" {
		return []string{r.System}
	}
	staticPrompt, dynamicPlan := BuildSystemParts(r.Env, r.Model)
	staticWithHarness := ApplyHarnessToSystem(staticPrompt, GetHarnessProfile(r.Harness))
	if r.ToolSearch {
		staticWithHarness += "\n\n" + toolSearchHint
	}
	if strings.TrimSpace(dynamicPlan) != "" {
		return []string{staticWithHarness, dynamicPlan}
	}
	return []string{staticWithHarness}
}

// observeToolResult folds one finished tool call into the verify ledger.
func (r *Runner) observeToolResult(ledger *VerifyLedger, guard *loopGuard, name, rawArgs string, result Result) {
	lowered := strings.ToLower(strings.TrimSpace(name))
	switch lowered {
	case "apply_patch", "patch":
		if result.IsError {
			return
		}
		args, err := decodeToolArguments(rawArgs)
		if err != nil {
			return
		}
		for _, path := range patchChangedPaths(argString(args, "patch")) {
			*ledger = ledger.RecordEdit(path)
		}
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

// patchChangedPaths extracts the file paths touched by an apply_patch
// document, so verify-on-stop can track multi-file edits the same way it
// tracks exact-string tools.
func patchChangedPaths(document string) []string {
	ops, err := parsePatch(document)
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(ops))
	seen := make(map[string]bool, len(ops))
	for _, op := range ops {
		path := strings.TrimSpace(op.path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

// needsApprovalFor reports whether a call must wait. Trust is the outer
// gate: a mutating tool in an untrusted folder asks even when the approval
// mode would allow it - unless the operator has answered this folder's gate
// for this tool already. The app records those answers on
// Env.SessionAllowed and re-seeds them into every turn's Env, because a
// "session" or "always" answer that never silences the gate is a dead
// promise and the same tool re-asks every turn. Tool-level policy allowances
// are not that silencer: they are statements about a tool, not about this
// folder. In a trusted folder the allowlist runs.
func (r *Runner) needsApprovalFor(tool Tool) bool {
	if r.Policy != nil && r.Policy.NeedsApproval(tool) {
		return true
	}
	if tool.Mutating() && r.Env != nil && !r.Env.Trusted && !r.Env.SessionAllowed[tool.Name()] {
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
			Output:  unknownToolMessage(call.Name, r.toolNamesList(), r.ToolSearch),
			IsError: true,
		}
	}

	startLabel := tool.Label(args)
	emit := r.emit
	// The diff preview is computed before the run, not after: it locates
	// old_string in the file as it is now, and a successful edit makes that
	// text history. Tools outside the edit family answer with an empty
	// preview, so this costs nothing they did not already pay - the approval
	// dialog was building the same diff anyway.
	preview := ""
	if r.Env != nil {
		preview = PreviewToolDiff(r.Env, tool.Name(), args)
	}
	emit(Event{Kind: EventToolStart, ToolName: tool.Name(), ToolLabel: startLabel, ToolArgs: Shorten(call.Arguments, 240), Preview: preview})

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
				Diff:   preview,
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
		if r.Policy != nil && r.Policy.Memory != nil {
			r.Policy.Memory.RecordApprovalDecision(tool.Name(), decision)
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

func (r *Runner) toolNamesList() []string {
	names := make([]string, 0, len(r.Tools.Tools()))
	for _, tool := range r.Tools.Tools() {
		names = append(names, tool.Name())
	}
	return names
}

// stepTools is the registry offered for one step. Without tool search it is the
// full allowlist. With it, only the always-on loop and the tools the model has
// discovered are advertised; every tool stays callable, so a discovered name
// never fails to resolve.
func (r *Runner) stepTools(discovered map[string]bool) *Registry {
	if !r.ToolSearch {
		return r.Tools
	}
	tools := make([]Tool, 0, len(r.Tools.Tools()))
	for _, tool := range r.Tools.Tools() {
		if toolSearchAlwaysOn[strings.ToLower(tool.Name())] || discovered[strings.ToLower(tool.Name())] {
			tools = append(tools, tool)
		}
	}
	return NewRegistry(tools...)
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
