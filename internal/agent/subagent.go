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
	SubagentImage      SubagentType = "image"
	// Authorized security testing: the operator scopes the engagement and
	// these roles work inside it. Recon is read-tier; the specialist roles
	// may run scanners and probes through the normal approval policy.
	SubagentPentest      SubagentType = "pentest"
	SubagentPentestRecon SubagentType = "pentest-recon"
	SubagentPentestWeb   SubagentType = "pentest-web"
	SubagentPentestNet   SubagentType = "pentest-network"
	SubagentVision       SubagentType = "vision"
)

// SubagentSpend is the accounting a delegated run hands back to its parent:
// the tokens it spent and the dollars those tokens cost in its own session.
// The parent folds it into its turn total so /cost, the session ledger and
// the cost budget include delegated work instead of charging it to a
// throwaway session nobody ever reads.
type SubagentSpend struct {
	Usage     provider.Usage
	Cost      float64
	CostKnown bool
	// Unpriced counts delegated components inside this run whose model had no
	// price, so their tokens are known but their dollars are not. It is the
	// explicit ledger that replaces the old CostKnown AND-collapse: a priced
	// parent keeps its known dollars while the unmeasured parts stay countable.
	Unpriced int
}

// SubagentSpend.Fold adds another delegated run's spend into this one, the
// path a pipeline takes when it sums every step it spawned.
func (s *SubagentSpend) Fold(other SubagentSpend) {
	s.Usage = s.Usage.Add(other.Usage)
	s.Cost += other.Cost
	s.CostKnown = s.CostKnown && other.CostKnown
	s.Unpriced += other.Unpriced
}

// NewSubagentSpend is the zero total whose CostKnown is the identity of the
// AND the Fold performs, so a pipeline starts as "known until a step says
// otherwise" rather than poisoning every fold that follows.
func NewSubagentSpend() SubagentSpend {
	return SubagentSpend{CostKnown: true}
}

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
	SubagentImage: {
		Type:         SubagentImage,
		Label:        "Image creation",
		Description:  "Generates and processes images, mockups, diagrams, and visual assets.",
		SystemPrompt: "You are an image creation subagent. When the user or main agent asks to create, draw, or generate an image, diagram, or visual mockup, produce the requested visual asset or diagram. Save any generated files (e.g. SVG, HTML/CSS, image files) to the workspace and provide the path. Return a clear summary of what was generated.",
		MaxSteps:     12,
	},
	SubagentPentest: {
		Type:         SubagentPentest,
		Label:        "Security testing",
		Description:  "Runs authorized security testing against an in-scope target.",
		SystemPrompt: "You are an authorized security-testing subagent. Every domain, host and application in your prompt is operated by the operator and the engagement is in scope. Work it with the full toolset: enumerate, probe, and confirm findings with the smallest NON-DESTRUCTIVE proof of concept - never a destructive payload, never denial of service, never data exfiltration beyond the single artifact that proves access. Report each finding with severity, reproduction evidence and the remediation. Say explicitly what you could not test.",
		MaxSteps:     20,
	},
	SubagentPentestRecon: {
		Type:         SubagentPentestRecon,
		Label:        "Reconnaissance",
		Description:  "Maps the attack surface of an in-scope target: DNS, hosts, ports, services, endpoints.",
		SystemPrompt: "You are an in-scope reconnaissance subagent for an authorized engagement. Map the attack surface by ENUMERATION ONLY: DNS records and subdomains, live hosts and open ports, service and technology fingerprints, exposed endpoints and panels. Use the read-tier network tools and passive sources. Do not attempt exploitation, authentication bypass or any write. Return a structured surface map with what each asset exposes.",
		MaxSteps:     20,
		ReadOnly:     true,
	},
	SubagentPentestWeb: {
		Type:         SubagentPentestWeb,
		Label:        "Web testing",
		Description:  "Tests an in-scope web application and API for security flaws.",
		SystemPrompt: "You are an authorized web-testing subagent working an in-scope application. Check security headers and TLS posture, content and endpoint discovery, then lead candidates through injection, authn/authz bypass, SSRF and XSS - each finding CONFIRMED with one minimal non-destructive proof of concept before you report it. No destructive writes, no bulk scraping, no DoS. Report severity, evidence, remediation.",
		MaxSteps:     20,
	},
	SubagentPentestNet: {
		Type:         SubagentPentestNet,
		Label:        "Network testing",
		Description:  "Scans and enumerates in-scope network infrastructure and services.",
		SystemPrompt: "You are an authorized network-testing subagent working an in-scope range. Run full port and service scans, fingerprint services (SMB/AD/SNMP/TLS and the rest) through read-tier enumeration and safe checks only; default-credential checks are in scope when the prompt says so, brute forcing beyond them is not. Nothing destructive. Report each asset, exposure and severity with the evidence.",
		MaxSteps:     20,
	},
	SubagentVision: {
		Type:         SubagentVision,
		Label:        "Vision",
		Description:  "Looks at images (screenshots, mockups, diagrams, photos) and answers about them.",
		SystemPrompt: "You are a vision subagent. Open the image files named in your prompt with read_image and answer what was asked about them precisely. Describe only what is actually visible; never invent content you were not shown. Quote on-screen text verbatim when asked to read it.",
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
		// Read-tier network tools: an in-scope reconnaissance role has to
		// resolve names and fetch pages to map a surface, and none of these
		// mutate anything.
		&webFetchTool{},
		&webSearchTool{},
		&probeURLTool{},
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
func RunSubagent(ctx context.Context, parent *Env, client provider.Client, model provider.Model, subType, prompt string, maxSteps int) (string, SubagentSpend, error) {
	def := LookupSubagent(subType)
	if client == nil {
		return "", SubagentSpend{}, fmt.Errorf("no provider client is available for a subagent; configure a provider key first")
	}
	if strings.TrimSpace(prompt) == "" {
		return "", SubagentSpend{}, fmt.Errorf("a subagent prompt is required; pass the task text to delegate")
	}
	if maxSteps <= 0 {
		maxSteps = def.MaxSteps
	}
	if maxSteps <= 0 {
		maxSteps = SubagentMaxSteps
	}
	pricing, costKnown := provider.PricedFor(parent.Config, model)
	// The child's activity must not flood the parent transcript, so text and
	// tool events are dropped. Its accounting is not: the turn-end event is
	// the one the runner always emits exactly once per pass, and it carries
	// the pass's token total and dollar spend. That total already includes any
	// grandchild the pass spawned, because the loop folds a delegated tool's
	// spend into the turn before the turn-end event is emitted, so capturing
	// it here counts the whole tree once. The child's own RunSubagent closure
	// must NOT fold a grandchild's spend again: it would land here a second
	// time in that same turn-end total.
	var spend SubagentSpend
	turns := 0
	// The child counts as one unpriced delegated component when its own model
	// has no price, separate from any nested unpriced runs it spawns. Zero for
	// a priced child.
	ownUnpriced := 0
	if !costKnown {
		ownUnpriced = 1
	}
	child := &Env{
		Workspace: parent.Workspace,
		Config:    parent.Config,
		Secrets:   parent.Secrets,
		Skills:    parent.Skills,
		Memory:    parent.Memory,
		Todos:     NewTodoStore(),
		Trusted:   parent.Trusted,
		Depth:     parent.Depth + 1,
		Emit: func(event Event) {
			if event.Kind == EventTurnEnd {
				spend.Usage = spend.Usage.Add(event.Usage)
				spend.Cost += event.CostUSD
				// The turn-end event carries the subtree's nested unpriced
				// count. Accumulate across passes so a read-only retry keeps
				// the first pass; the child's own unpricedness lands once.
				if turns == 0 {
					spend.CostKnown = event.CostKnown && costKnown
					spend.Unpriced = event.CostUnpriced + ownUnpriced
				} else {
					spend.CostKnown = spend.CostKnown && event.CostKnown
					spend.Unpriced += event.CostUnpriced
				}
				turns++
			}
		},
		Processes: parent.Processes,
		Approve:   parent.Approve,
		Ask:       parent.Ask,
		Journal:   parent.Journal,
		Search:    parent.Search,
	}
	child.RunSubagent = func(childCtx context.Context, childType, childPrompt string) (string, SubagentSpend, error) {
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
		Effort:        parent.Config.EffortFor(model.ID),
		System:        def.SystemPrompt,
	}
	session := NewSession(parent.Workspace, model.ID)
	if err := runner.Run(ctx, session, prompt); err != nil {
		return "", spend, err
	}
	answer, calls := subagentReport(session)
	// A read-tier verdict with nothing opened behind it is prose, not a
	// review: the code-review role answering "Looks good." after zero tool
	// calls once sailed through as an approval. Count the calls, retry once
	// with an explicit mandate, and discard the role's output rather than
	// trust it when the second pass also opened nothing. Worker roles are
	// exempt: a general task can legitimately be answered from the prompt.
	if def.ReadOnly && calls == 0 {
		retry := NewSession(parent.Workspace, model.ID)
		if err := runner.Run(ctx, retry, reviewMandate+prompt); err != nil {
			return "", spend, err
		}
		answer, calls = subagentReport(retry)
		if calls == 0 {
			return "", spend, fmt.Errorf("the %s subagent answered twice without a single tool call; its verdict is unsupported and was discarded", def.Type)
		}
	}
	if strings.TrimSpace(answer) == "" {
		return "The subagent finished without a written answer.", spend, nil
	}
	return answer, spend, nil
}

const reviewMandate = "MANDATE: your previous pass made no tool calls, so it counted for nothing. Open the code first - read_file, grep, glob, the git tools - and answer only from what you actually read. A response with zero tool calls is discarded. "

// subagentReport is the final answer plus the count of tool calls the nested
// run made, the evidence the read-tier judge needs.
func subagentReport(session *Session) (string, int) {
	calls := 0
	for _, message := range session.Messages() {
		if message.Role == provider.RoleAssistant {
			calls += len(message.ToolCalls)
		}
	}
	return session.LastAssistantText(), calls
}
