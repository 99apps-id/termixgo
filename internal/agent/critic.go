package agent

import (
	"sort"
	"strings"
)

// CriticNudgePrefix marks a continuation as a planner-executor-critic review,
// so it keeps the todo list instead of starting a fresh task.
const CriticNudgePrefix = "[System: critic]"

// MaxCriticNudges bounds synthetic critic follow-ups per turn. One review
// pass is enough for the smallest increment; the verify ledger keeps its own
// separate bound for verification nudges.
const MaxCriticNudges = 1

// CriticVerdict is the deterministic outcome of the critic review.
type CriticVerdict struct {
	// BlockFinish is true when the turn must not stop yet.
	BlockFinish bool
	// Nudge is the synthetic follow-up when BlockFinish is true.
	Nudge string
}

// ReviewFinish inspects the plan and the verification ledger before the turn
// stops. It blocks finishing when:
//
//   - the todo plan still has an active or pending item, or
//   - code was edited without fresh passing verification evidence, or
//   - the reply claims verification without recorded evidence.
//
// The check is purely deterministic: it reads TodoStore items and the
// VerifyLedger, never the model. attempts counts critic nudges already sent
// this turn and caps them at MaxCriticNudges.
func ReviewFinish(todos []Todo, ledger VerifyLedger, claimedVerification bool, attempts int) CriticVerdict {
	return ReviewFinishWithHints(todos, ledger, claimedVerification, attempts, nil)
}

// ReviewFinishWithHints behaves like ReviewFinish and additionally folds
// known journal fixes for the tools that failed this turn into the nudge, so
// the model gets the recorded remedy instead of rediscovering it.
func ReviewFinishWithHints(todos []Todo, ledger VerifyLedger, claimedVerification bool, attempts int, hints map[string][]string) CriticVerdict {
	if attempts >= MaxCriticNudges {
		return CriticVerdict{}
	}
	open := make([]string, 0)
	for _, item := range todos {
		switch strings.ToLower(strings.TrimSpace(item.Status)) {
		case "in_progress":
			open = append(open, "- `[>] "+item.Title+"` (in_progress)")
		case "pending":
			open = append(open, "- `[ ] "+item.Title+"` (pending)")
		}
	}
	needsVerify := ledger.ShouldNudgeVerification(claimedVerification)
	if len(open) == 0 && !needsVerify {
		return CriticVerdict{}
	}
	var builder strings.Builder
	builder.WriteString(CriticNudgePrefix + " Before finishing, the critic review found open work:")
	if len(open) > 0 {
		builder.WriteString("\n\nUnfinished plan items:\n" + strings.Join(open, "\n"))
	}
	if needsVerify {
		builder.WriteString("\n\nVerification gap: code was edited (or verification was claimed) without fresh passing evidence since the last edit.")
	}
	verdict := strings.TrimSpace(ledger.BuildVerifyNudge(0, claimedVerification))
	if verdict != "" {
		// Reuse the ledger's concrete instruction so the critic and the
		// verify gate speak the same language instead of diverging.
		detail := strings.TrimPrefix(verdict, VerifyNudgePrefix)
		builder.WriteString("\n\nWhat to do next:" + detail)
	} else {
		builder.WriteString("\n\nFinish the open items or mark them complete with todo_write, run the smallest relevant verification (the run_checks tool, or the project test, lint or build command), then summarize what is verified.")
	}
	if learned := formatLearnedFixes(hints); learned != "" {
		builder.WriteString("\n\nKnown fixes from earlier runs:\n" + learned)
	}
	return CriticVerdict{BlockFinish: true, Nudge: builder.String()}
}

// formatLearnedFixes renders journal hints keyed by tool name, capped so one
// noisy tool cannot flood the nudge.
func formatLearnedFixes(hints map[string][]string) string {
	if len(hints) == 0 {
		return ""
	}
	tools := make([]string, 0, len(hints))
	for tool := range hints {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	lines := make([]string, 0)
	for _, tool := range tools {
		seen := map[string]bool{}
		count := 0
		for _, hint := range hints[tool] {
			hint = strings.TrimSpace(hint)
			if hint == "" || seen[hint] {
				continue
			}
			seen[hint] = true
			lines = append(lines, "- "+tool+": "+hint)
			if count++; count >= 3 {
				break
			}
		}
		if len(lines) >= 6 {
			break
		}
	}
	return strings.Join(lines, "\n")
}

// PlanNudgePrefix marks a mid-turn continuation that asks for a plan, so it
// keeps the todo list instead of starting a fresh task.
const PlanNudgePrefix = "[System: plan-first]"

// MaxPlanNudges bounds synthetic plan reminders per turn. One reminder is
// enough: repeating it would nag a model that deliberately works plan-free on
// a trivial edit.
const MaxPlanNudges = 1

// NeedsPlanReminder reports whether a successful code mutation happened with
// no active or pending plan item. todos is the current plan snapshot;
// mutated is true when the just-finished tool call edited verifiable code.
// attempts counts plan nudges already sent this turn.
func NeedsPlanReminder(todos []Todo, mutated bool, attempts int) bool {
	if !mutated || attempts >= MaxPlanNudges {
		return false
	}
	for _, item := range todos {
		switch strings.ToLower(strings.TrimSpace(item.Status)) {
		case "in_progress", "pending":
			return false
		}
	}
	return true
}

// BuildPlanNudge renders the plan-first reminder, or empty when silent.
func BuildPlanNudge(attempts int) string {
	if attempts >= MaxPlanNudges {
		return ""
	}
	return PlanNudgePrefix + " You just edited code with no active plan. For any task with three or more steps, record the plan with todo_write and keep exactly one item in_progress. If this was a trivial single edit, continue without a plan."
}

// CriticSummary renders a one-line verdict for the notice stream.
func CriticSummary(v CriticVerdict) string {
	if !v.BlockFinish {
		return ""
	}
	return "Critic review blocked the finish: open plan or missing verification evidence."
}
