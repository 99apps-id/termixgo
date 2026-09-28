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

// Runner drives one conversation: it streams from the provider, executes the
// tools the model requests, and reports every step through Env.Emit.
type Runner struct {
	Client   provider.Client
	Model    string
	Config   config.Config
	Env      *Env
	Tools    *Registry
	Policy   *ApprovalPolicy
	MaxSteps int
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
}

// Run executes one operator turn to completion.
func (r *Runner) Run(ctx context.Context, session *Session, input string) error {
	emit := r.emit
	maxSteps := r.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 25
	}

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
			session.AddUsage(turnUsage)
			session.SetCost(sessionCost)
			emit(Event{Kind: EventTurnEnd, StopReason: "stop", Usage: turnUsage, CostUSD: sessionCost, CostKnown: costKnown})
			return nil
		}

		for _, call := range calls {
			if ctx.Err() != nil {
				stopReason = "aborted"
				break
			}
			result := r.execute(ctx, call)
			session.AddToolResult(call.ID, call.Name, result.Output)
			if ctx.Err() != nil {
				stopReason = "aborted"
				break
			}
		}
		if ctx.Err() != nil {
			break
		}
	}

	if stopReason == "stop" {
		// Only reached when the step budget was exhausted without a stop.
		stopReason = "step-cap"
		emit(Event{Kind: EventNotice, Text: fmt.Sprintf("Reached the step budget (%d). Reply to continue.", maxSteps)})
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

// system assembles the system prompt for this step.
func (r *Runner) system(session *Session) string {
	if strings.TrimSpace(r.System) != "" {
		return r.System
	}
	return BuildSystem(r.Env, r.Model)
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

	if r.Policy != nil && r.Policy.NeedsApproval(tool) {
		decision := DecisionDeny
		if r.Env.Approve != nil {
			decision = r.Env.Approve(ApprovalRequest{
				Tool:   tool.Name(),
				Detail: startLabel,
				Risk:   string(tool.Risk()),
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
