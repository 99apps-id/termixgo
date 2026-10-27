package config

import (
	"regexp"
	"strings"
)

// AgentTools is the per-agent tool policy. Deny removes tools from the
// registry the agent's turns are offered; it can never remove a tool the
// approval policy still expects, because the policy names tools, not the
// other way around.
type AgentTools struct {
	Deny []string `json:"deny,omitempty"`
}

// Agent is one named persona: an optional model and workspace plus the
// per-agent switches. An entry with no fields is the plain default agent.
type Agent struct {
	// Model overrides the active model for turns of this agent, as a
	// catalogue id or "provider:model". Empty means the active model.
	Model string `json:"model,omitempty"`
	// Workspace is where this agent reads and writes. Empty means the
	// directory Termixgo was launched in for the default agent, and the
	// workspace directory inside the agent home for every other agent.
	Workspace string `json:"workspace,omitempty"`
	// ApprovalMode overrides the global approval policy for this agent.
	ApprovalMode string `json:"approvalMode,omitempty"`
	// Tools narrows the tool registry this agent's turns are offered.
	Tools AgentTools `json:"tools,omitempty"`
	// Grounding is "strict" when an answer without tool evidence is challenged
	// once and marked. Empty means normal.
	Grounding string `json:"grounding,omitempty"`
	// Delegation bounds how far this agent may fan out.
	Delegation Delegation `json:"delegation,omitempty"`
}

// Delegation bounds one agent's subagents. Zero uses the defaults: three
// children per turn and the runner's own nesting cap. A negative value means
// no extra limit beyond the runner's own.
type Delegation struct {
	MaxChildren int `json:"maxChildren,omitempty"`
	MaxDepth    int `json:"maxDepth,omitempty"`
	// AutoApprove unset means yes, which is the behaviour before the switch;
	// false asks the operator before a delegation starts.
	AutoApprove *bool `json:"autoApprove,omitempty"`
}

// DefaultDelegationChildren is the per-turn cap when the config leaves it at
// zero.
const DefaultDelegationChildren = 3

// DelegationLimits resolves the effective caps: children <= 0 means no cap,
// depth <= 0 means the runner's own nesting cap.
func (c Config) DelegationLimits(name string) (children, depth int) {
	entry := c.Agent(name).Delegation
	switch {
	case entry.MaxChildren > 0:
		children = entry.MaxChildren
	case entry.MaxChildren == 0:
		children = DefaultDelegationChildren
	}
	if entry.MaxDepth > 0 {
		depth = entry.MaxDepth
	}
	return children, depth
}

// DelegationAutoApprove reports whether a subagent may start without an
// approval prompt. Unset means yes.
func (c Config) DelegationAutoApprove(name string) bool {
	if entry := c.Agent(name).Delegation.AutoApprove; entry != nil {
		return *entry
	}
	return true
}

// Grounding modes.
const (
	GroundingNormal = "normal"
	GroundingStrict = "strict"
)

// NormalizeGrounding folds a hand-edited value onto the accepted set.
func NormalizeGrounding(raw string) string {
	if strings.ToLower(strings.TrimSpace(raw)) == GroundingStrict {
		return GroundingStrict
	}
	return GroundingNormal
}

// AgentGrounding is the grounding mode of one agent's turns. A guest run is
// always strict, which the caller applies.
func (c Config) AgentGrounding(name string) string {
	return NormalizeGrounding(c.Agent(name).Grounding)
}

// AgentApprovalMode resolves the approval mode one agent's turns use: its own
// valid override, else the global setting.
func (c Config) AgentApprovalMode(name string) ApprovalMode {
	if mode := ApprovalMode(strings.TrimSpace(c.Agent(name).ApprovalMode)); ValidApprovalMode(mode) {
		return mode
	}
	return c.ApprovalMode
}

// FinalAgentName is the name every config has, whether it was configured or
// not: the operator's own terminal agent.
const FinalAgentName = "default"

// agentNamePattern keeps a name usable as a directory component and as part of
// a session id: lowercase letters, digits, underscore and dash, at most 32.
var agentNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ValidAgentName reports whether a name is one this build accepts.
func ValidAgentName(name string) bool {
	return agentNamePattern.MatchString(strings.TrimSpace(name))
}

// Agent returns the entry for a name, or the zero value.
func (c Config) Agent(name string) Agent {
	return c.Agents[strings.TrimSpace(name)]
}

// HasAgent reports whether the name is configured.
func (c Config) HasAgent(name string) bool {
	_, ok := c.Agents[strings.TrimSpace(name)]
	return ok
}

// WithAgent returns a copy with one entry set. The map is copied rather than
// mutated: a config value is passed around by copy and a shared map would make
// an edit in one place appear in another.
func (c Config) WithAgent(name string, agent Agent) Config {
	trimmed := strings.TrimSpace(name)
	if !ValidAgentName(trimmed) {
		return c
	}
	next := make(map[string]Agent, len(c.Agents)+1)
	for key, value := range c.Agents {
		next[key] = value
	}
	agent.Model = strings.TrimSpace(agent.Model)
	if workspace := strings.TrimSpace(agent.Workspace); workspace != "" {
		agent.Workspace = CleanFolder(workspace)
	}
	next[trimmed] = agent
	c.Agents = next
	return c
}

// WithoutAgent removes one entry, reporting whether it existed.
func (c Config) WithoutAgent(name string) (Config, bool) {
	trimmed := strings.TrimSpace(name)
	if _, ok := c.Agents[trimmed]; !ok {
		return c, false
	}
	next := make(map[string]Agent, len(c.Agents))
	for key, value := range c.Agents {
		if key == trimmed {
			continue
		}
		next[key] = value
	}
	c.Agents = next
	if c.ActiveAgent == trimmed {
		c.ActiveAgent = FinalAgentName
	}
	return c, true
}

// normaliseAgents repairs the agent registry a hand-edited file could break.
// An invalid name is dropped rather than kept: the name is a directory
// component, and a bad one would fail on first use instead of here.
func (c *Config) normaliseAgents() {
	cleaned := make(map[string]Agent, len(c.Agents)+1)
	for name, entry := range c.Agents {
		trimmed := strings.TrimSpace(name)
		if !ValidAgentName(trimmed) {
			continue
		}
		entry.Model = strings.TrimSpace(entry.Model)
		if workspace := strings.TrimSpace(entry.Workspace); workspace != "" {
			entry.Workspace = CleanFolder(workspace)
		}
		entry.ApprovalMode = strings.ToLower(strings.TrimSpace(entry.ApprovalMode))
		if entry.ApprovalMode != "" && !ValidApprovalMode(ApprovalMode(entry.ApprovalMode)) {
			entry.ApprovalMode = ""
		}
		entry.Tools.Deny = trimList(entry.Tools.Deny)
		entry.Grounding = strings.ToLower(strings.TrimSpace(entry.Grounding))
		if entry.Grounding != GroundingStrict {
			entry.Grounding = ""
		}
		cleaned[trimmed] = entry
	}
	if _, ok := cleaned[FinalAgentName]; !ok {
		cleaned[FinalAgentName] = Agent{}
	}
	c.Agents = cleaned

	c.ActiveAgent = strings.TrimSpace(c.ActiveAgent)
	if _, ok := cleaned[c.ActiveAgent]; !ok {
		c.ActiveAgent = FinalAgentName
	}
}
