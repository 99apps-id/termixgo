package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/99apps-id/termixgo/internal/provider"
)

// SubagentType names one worker role. The types differ only in the system
// prompt they are pointed at, not in what they may touch: like Termigo, a
// subagent is a peer of the main agent with the full toolset and no sandbox.
// Only the nesting cap withholds the spawn tool.
type SubagentType string

const (
	SubagentExplore    SubagentType = "explore"
	SubagentGeneral    SubagentType = "general"
	SubagentBuilder    SubagentType = "builder"
	SubagentCodeReview SubagentType = "code-review"
	SubagentSecurity   SubagentType = "security"
)

// MaxSubagentDepth caps nesting. The main agent runs at depth 0 and may
// spawn down to this depth; at the cap the spawn tool is withheld.
const MaxSubagentDepth = 3

// SubagentMaxSteps is the default budget for one delegated run.
const SubagentMaxSteps = 12

// SubagentDef describes one worker role.
type SubagentDef struct {
	Type         SubagentType
	Label        string
	Description  string
	SystemPrompt string
	MaxSteps     int
	ReadOnly     bool
}

// Subagents is the roster, mirroring the Termigo set in scope.
var Subagents = map[SubagentType]SubagentDef{
	SubagentExplore: {
		Type:         SubagentExplore,
		Label:        "Explore",
		Description:  "Codebase explorer. Reads first and acts when the task needs it.",
		SystemPrompt: "You are an exploration subagent. Answer the spawn question primarily by READING the codebase. You may edit or run a command when the task genuinely requires it. Verify, do not speculate. Return a concise summary with file paths and line numbers. Stop as soon as you can answer.",
	},
	SubagentGeneral: {
		Type:         SubagentGeneral,
		Label:        "General",
		Description:  "General-purpose worker for a self-contained task.",
		SystemPrompt: "You are a general-purpose subagent. Carry out the self-contained task in your prompt end to end. Verify, do not speculate. You have the full toolset. Return a tight summary with the evidence you used and anything you could not finish.",
	},
	SubagentBuilder: {
		Type:         SubagentBuilder,
		Label:        "Builder",
		Description:  "Implements one self-contained piece of work.",
		SystemPrompt: "You are a builder subagent. Implement ONE self-contained piece of work described in your prompt, then stop. Read before you write. Stay inside the files your prompt names. Verify with the smallest check and return a short summary of files changed and anything unfinished.",
	},
	SubagentCodeReview: {
		Type:         SubagentCodeReview,
		Label:        "Code review",
		Description:  "Reviews changed code for correctness, architecture, performance and security.",
		SystemPrompt: "You are a code-review subagent. Inspect the requested code and report only ACTIONABLE findings: correctness bugs, architecture violations, performance issues, security risks. Skip style. Format each finding as severity, file:line, issue, fix. If nothing is wrong, say Looks good.",
		MaxSteps:     12,
		ReadOnly:     true,
	},
	SubagentSecurity: {
		Type:         SubagentSecurity,
		Label:        "Security review",
		Description:  "Audits code and configuration for security risks.",
		SystemPrompt: "You are a security-review subagent. Scan the requested scope for injection, auth bypass, secret leakage, missing validation at trust boundaries, unsafe deserialization and weak crypto. Report concrete findings with file:line and severity. If nothing is wrong, say No security issues found.",
		ReadOnly:     true,
	},
}

// LookupSubagent resolves a type, falling back to the general worker so a
// free-form string never has to handle a miss.
func LookupSubagent(raw string) SubagentDef {
	trimmed := SubagentType(strings.ToLower(strings.TrimSpace(raw)))
	if def, ok := Subagents[trimmed]; ok {
		return def
	}
	return Subagents[SubagentGeneral]
}

// SubagentIsReadOnly reports whether a role is read-tier by design.
func SubagentIsReadOnly(raw string) bool { return LookupSubagent(raw).ReadOnly }

// readOnlyTools are the tools a review role may use: it can look, never touch.
func readOnlyTools() *Registry {
	return NewRegistry(
		&readFileTool{},
		&listDirectoryTool{},
		&grepTool{},
		&globTool{},
		&readImageTool{},
		&findSkillTool{},
		&useSkillTool{},
		&thinkTool{},
		&gitStatusTool{},
		&gitDiffTool{},
		&gitLogTool{},
		&gitShowTool{},
	)
}

// subagentRegistry builds the toolset for one role. Review roles are
// read-only by design; every other role gets the full allowlist with no
// sandbox, and the spawn tool is withheld at the nesting cap.
func subagentRegistry(subType string, depth int) *Registry {
	if SubagentIsReadOnly(subType) {
		return readOnlyTools()
	}
	tools := make([]Tool, 0, len(DefaultRegistry().Tools()))
	for _, tool := range DefaultRegistry().Tools() {
		name := strings.ToLower(tool.Name())
		if depth >= MaxSubagentDepth && (name == "run_subagent" || name == "task" || name == "delegate") {
			continue
		}
		tools = append(tools, tool)
	}
	return NewRegistry(tools...)
}

// RunSubagent runs a nested investigation of one typed role and returns its
// final answer. The nested run gets its own session and todo list, so a
// large search cannot flood the parent context.
//
// The child inherits the parent's budget, pricing and approval mode rather
// than running free: an unpriced default silently disables the cost cap, an
// oversized default history overflows a small local window, and a hardcoded
// allow-all policy let plan mode mutate through a subagent.
func RunSubagent(ctx context.Context, parent *Env, client provider.Client, model provider.Model, subType, prompt string, maxSteps int) (string, error) {
	def := LookupSubagent(subType)
	if client == nil {
		return "", fmt.Errorf("no provider client is available for a subagent")
	}
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("a subagent prompt is required")
	}
	if maxSteps <= 0 {
		maxSteps = def.MaxSteps
	}
	if maxSteps <= 0 {
		maxSteps = SubagentMaxSteps
	}
	pricing, costKnown := provider.PricedFor(parent.Config, model)
	child := &Env{
		Workspace: parent.Workspace,
		Config:    parent.Config,
		Secrets:   parent.Secrets,
		Skills:    parent.Skills,
		Memory:    parent.Memory,
		Todos:     NewTodoStore(),
		Trusted:   parent.Trusted,
		Depth:     parent.Depth + 1,
		Emit:      nil,
		Processes: parent.Processes,
		Approve:   parent.Approve,
		Ask:       parent.Ask,
		Journal:   parent.Journal,
		Search:    parent.Search,
	}
	child.RunSubagent = func(childCtx context.Context, childType, childPrompt string) (string, error) {
		return RunSubagent(childCtx, child, client, model, childType, childPrompt, maxSteps)
	}
	runner := &Runner{
		Client:        client,
		Model:         provider.WireModel(parent.Config, model),
		Config:        parent.Config,
		Env:           child,
		Tools:         subagentRegistry(string(def.Type), parent.Depth),
		Policy:        &ApprovalPolicy{Mode: ApprovalModeOrDefault(parent.Config)},
		MaxSteps:      maxSteps,
		Harness:       string(parent.Config.HarnessProfile),
		ContextBudget: HistoryBudget(model.Window()),
		Pricing:       pricing,
		CostKnown:     costKnown,
		CostBudgetUSD: parent.Config.CostBudgetUSD,
		ToolSearch:    parent.Config.ToolSearchEnabled,
		System:        def.SystemPrompt,
	}
	session := NewSession(parent.Workspace, model.ID)
	if err := runner.Run(ctx, session, prompt); err != nil {
		return "", err
	}
	answer := session.LastAssistantText()
	if strings.TrimSpace(answer) == "" {
		return "The subagent finished without a written answer.", nil
	}
	return answer, nil
}
