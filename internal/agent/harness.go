package agent

import (
	"sort"
	"strings"
)

// HarnessProfile names one way to run the agent loop: prompt prelude, tool
// order and step budget. It mirrors the Termigo harness profiles so a
// workspace behaves the same in both front ends.
type HarnessProfile struct {
	ID              string
	Label           string
	Description     string
	PromptPrelude   string
	PrioritizeTools []string
	HideTools       []string
	StepBudgetDelta int
	StepBudgetCap   int
}

// DefaultHarnessProfile is critical: evidence first, frugal with context.
const DefaultHarnessProfile = "critical"

// DefaultStepBudget bounds one turn when nothing else is configured. It is
// generous because the budget pauses a turn rather than ending a task: the
// operator replies and the work continues, while the loop guard and the cost
// cap stay the real stops for a run that goes nowhere. It is the whole turn's
// ceiling, so it is set high enough that one long task finishes in one reply.
const DefaultStepBudget = 300

// BuiltinHarnessProfiles ships the named profiles.
var BuiltinHarnessProfiles = map[string]HarnessProfile{
	"balanced": {
		ID:          "balanced",
		Label:       "Balanced",
		Description: "Default harness: no extra guidance, full toolset, standard loop.",
	},
	"plan_briefly": {
		ID:            "plan_briefly",
		Label:         "Plan first",
		Description:   "Short planning reminder before acting.",
		PromptPrelude: "Start with a short plan, then act. Avoid repeating environment discovery the prompt already covers.",
	},
	"verify_before_finish": {
		ID:            "verify_before_finish",
		Label:         "Verify before finish",
		Description:   "Run the smallest relevant verification and summarize the result before stopping.",
		PromptPrelude: "Before finishing, run the smallest relevant verification step you can (run_checks kind=test or kind=lint) and summarize the concrete result.",
	},
	"terminal_first": {
		ID:              "terminal_first",
		Label:           "Terminal-first",
		Description:     "Prefer shell and process tools earlier in the tool list for command-heavy work.",
		PrioritizeTools: []string{"run_command", "run_checks", "background", "logs", "wait", "list_processes", "kill"},
	},
	"shorter_loop": {
		ID:              "shorter_loop",
		Label:           "Shorter loop",
		Description:     "Cap the number of agent steps for short, bounded tasks.",
		StepBudgetDelta: -6,
		StepBudgetCap:   16,
	},
	"no_todo": {
		ID:          "no_todo",
		Label:       "No todo overhead",
		Description: "Hide the todo tools for lighter tasks.",
		HideTools:   []string{"todo_write", "todo_read"},
	},
	"autonomous": {
		ID:          "autonomous",
		Label:       "Fully autonomous",
		Description: "Self-directed execution with proactive failure pivoting; the configured step limit still applies.",
		PromptPrelude: "You are operating in FULLY AUTONOMOUS mode. Complete the request end to end and verify your work.\n" +
			"- Break an ambiguous goal into concrete milestones.\n" +
			"- Never assume a path, symbol or dependency exists without reading it first.\n" +
			"- On error or timeout, diagnose, pivot to an alternative, and continue. Do not stop early.\n" +
			"- Verify with inspection, test or lint before concluding.",
	},
	"critical": {
		ID:          "critical",
		Label:       "Critical and frugal",
		Description: "Adversarial self-review plus strict token economy: state assumptions, falsify edits, verify with the smallest check.",
		PromptPrelude: "Work in a critical, evidence-first mode.\n" +
			"- Treat your first idea and the user framing as a hypothesis. Ask what falsifies it before editing or concluding.\n" +
			"- Separate what you VERIFIED (a line you read, a command you ran) from what you ASSUMED.\n" +
			"- After an edit, re-read the changed region and run the smallest check that proves it. Never claim green without running it.\n" +
			"- Spend tokens like they cost money: prefer grep and glob with offset and limit over whole-file reads, and never echo file contents the tool call already carries.\n" +
			"- Be concise in prose and complete in substance: answer, name the evidence, stop.",
		PrioritizeTools: []string{"read_file", "grep", "glob", "edit", "multi_edit", "run_checks"},
	},
}

// GetHarnessProfile resolves an id, falling back to the critical default.
func GetHarnessProfile(id string) HarnessProfile {
	if profile, ok := BuiltinHarnessProfiles[strings.TrimSpace(id)]; ok {
		return profile
	}
	return BuiltinHarnessProfiles[DefaultHarnessProfile]
}

// HarnessProfiles lists the profiles in display order, default first, so the
// picker and the help text agree.
func HarnessProfiles() []HarnessProfile {
	ids := make([]string, 0, len(BuiltinHarnessProfiles))
	for id := range BuiltinHarnessProfiles {
		if id == DefaultHarnessProfile {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	profiles := make([]HarnessProfile, 0, len(ids)+1)
	profiles = append(profiles, BuiltinHarnessProfiles[DefaultHarnessProfile])
	for _, id := range ids {
		profiles = append(profiles, BuiltinHarnessProfiles[id])
	}
	return profiles
}

// HarnessProfileIDs lists every profile id in display order, which is what the
// command surfaces offer and what an error message names.
func HarnessProfileIDs() []string {
	profiles := HarnessProfiles()
	ids := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		ids = append(ids, profile.ID)
	}
	return ids
}

// RegistryForProfile applies a harness profile to a toolset.
//
// The profile changes order and visibility and never widens or narrows the
// allowlist on its own: there is no sandbox, and folder trust is the gate that
// keeps a mutating tool safe. The default profile hides nothing.
func RegistryForProfile(base *Registry, profile HarnessProfile) *Registry {
	if base == nil || (len(profile.HideTools) == 0 && len(profile.PrioritizeTools) == 0) {
		return base
	}
	byName := make(map[string]Tool, len(base.Tools()))
	names := make([]string, 0, len(base.Tools()))
	for _, tool := range base.Tools() {
		byName[strings.ToLower(tool.Name())] = tool
		names = append(names, tool.Name())
	}
	ordered := ApplyHarnessToTools(names, profile)
	tools := make([]Tool, 0, len(ordered))
	for _, name := range ordered {
		if tool, ok := byName[strings.ToLower(name)]; ok {
			tools = append(tools, tool)
		}
	}
	return NewRegistry(tools...)
}

// ApplyHarnessToSystem prepends the profile prelude to the system prompt.
func ApplyHarnessToSystem(base string, profile HarnessProfile) string {
	prelude := strings.TrimSpace(profile.PromptPrelude)
	if prelude == "" {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return prelude
	}
	return prelude + "\n\n" + base
}

// ApplyHarnessToTools reorders and hides tools per the profile. The allowlist
// stays explicit: every tool in the registry is allowed, the profile only
// changes order and visibility.
func ApplyHarnessToTools(names []string, profile HarnessProfile) []string {
	hidden := map[string]bool{}
	for _, name := range profile.HideTools {
		hidden[strings.ToLower(strings.TrimSpace(name))] = true
	}
	kept := names[:0:0]
	for _, name := range names {
		if !hidden[strings.ToLower(name)] {
			kept = append(kept, name)
		}
	}
	rank := map[string]int{}
	for index, name := range profile.PrioritizeTools {
		rank[strings.ToLower(strings.TrimSpace(name))] = index
	}
	if len(rank) > 0 {
		ordered := append([]string{}, kept...)
		sort.SliceStable(ordered, func(i, j int) bool {
			ri, oki := rank[strings.ToLower(ordered[i])]
			rj, okj := rank[strings.ToLower(ordered[j])]
			if oki && okj {
				return ri < rj
			}
			if oki {
				return true
			}
			if okj {
				return false
			}
			return ordered[i] < ordered[j]
		})
		return ordered
	}
	return kept
}

// ApplyHarnessToBudget adjusts a base step budget by the profile delta,
// capped when the profile sets a cap. The floor is 1 so a misconfigured
// profile cannot produce an unbounded loop guard that never fires.
//
// An unset budget becomes DefaultStepBudget rather than a small number: the
// budget bounds one turn, not the task, and a task that needs more simply
// continues when the operator replies.
func ApplyHarnessToBudget(base int, profile HarnessProfile) int {
	budget := base
	if budget <= 0 {
		budget = DefaultStepBudget
	}
	if profile.StepBudgetDelta < 0 {
		budget += profile.StepBudgetDelta
	}
	if budget < 1 {
		budget = 1
	}
	if profile.StepBudgetCap > 0 && budget > profile.StepBudgetCap {
		budget = profile.StepBudgetCap
	}
	return budget
}
