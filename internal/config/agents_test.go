package config

import (
	"testing"
)

func TestValidAgentName(t *testing.T) {
	valid := []string{"default", "sales", "agen-2", "support_bot", "a1", "x"}
	for _, name := range valid {
		if !ValidAgentName(name) {
			t.Errorf("ValidAgentName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", "Sales", "sales bot", "sales/bot", "../evil", "a.b", "-lead", "sales!"}
	for _, name := range invalid {
		if ValidAgentName(name) {
			t.Errorf("ValidAgentName(%q) = true, want false", name)
		}
	}
}

func TestNormaliseAgentsAddsDefaultAndRepairsActive(t *testing.T) {
	cfg := Default()
	cfg.Agents = map[string]Agent{"sales": {Model: "anthropic:claude-sonnet-4-5"}}
	cfg.ActiveAgent = "ghost"
	cfg.normalise()

	if !cfg.HasAgent(FinalAgentName) {
		t.Fatalf("the default agent must exist after normalisation")
	}
	if cfg.ActiveAgent != FinalAgentName {
		t.Errorf("ActiveAgent = %q, want %q", cfg.ActiveAgent, FinalAgentName)
	}
	if cfg.Agent("sales").Model != "anthropic:claude-sonnet-4-5" {
		t.Errorf("the sales model was lost: %+v", cfg.Agent("sales"))
	}
}

func TestNormaliseAgentsDropsInvalidNames(t *testing.T) {
	cfg := Default()
	cfg.Agents = map[string]Agent{
		"sales":    {},
		"Bad Name": {},
		"../evil":  {},
	}
	cfg.ActiveAgent = "sales"
	cfg.normalise()

	if cfg.HasAgent("Bad Name") || cfg.HasAgent("../evil") {
		t.Errorf("invalid agent names survived normalisation: %+v", cfg.Agents)
	}
	if cfg.ActiveAgent != "sales" {
		t.Errorf("ActiveAgent = %q, want sales", cfg.ActiveAgent)
	}
}

func TestWithAgentDoesNotMutateTheOriginal(t *testing.T) {
	base := Default()
	updated := base.WithAgent("sales", Agent{Workspace: "/tmp"})

	if base.HasAgent("sales") {
		t.Errorf("WithAgent mutated the receiver's map")
	}
	if !updated.HasAgent("sales") {
		t.Errorf("WithAgent did not add the entry")
	}
}

func TestWithAgentRejectsInvalidName(t *testing.T) {
	base := Default()
	updated := base.WithAgent("Bad Name", Agent{})
	if updated.HasAgent("Bad Name") {
		t.Errorf("an invalid name was accepted")
	}
}

func TestWithoutAgentResetsActive(t *testing.T) {
	cfg := Default().WithAgent("sales", Agent{})
	cfg.ActiveAgent = "sales"

	updated, ok := cfg.WithoutAgent("sales")
	if !ok {
		t.Fatalf("WithoutAgent reported the entry missing")
	}
	if updated.HasAgent("sales") {
		t.Errorf("the entry survived removal")
	}
	if updated.ActiveAgent != FinalAgentName {
		t.Errorf("ActiveAgent = %q, want %q", updated.ActiveAgent, FinalAgentName)
	}
	if !cfg.HasAgent("sales") {
		t.Errorf("WithoutAgent mutated the receiver")
	}
}

func TestNormaliseAgentSwitches(t *testing.T) {
	cfg := Default()
	cfg.ApprovalMode = ApprovalEdits
	cfg.Agents = map[string]Agent{
		"sales": {ApprovalMode: " PLAN ", Tools: AgentTools{Deny: []string{" web_search ", ""}}},
		"bad":   {ApprovalMode: "nonsense"},
	}
	cfg.normalise()

	sales := cfg.Agent("sales")
	if sales.ApprovalMode != "plan" {
		t.Errorf("ApprovalMode = %q, want plan", sales.ApprovalMode)
	}
	if len(sales.Tools.Deny) != 1 || sales.Tools.Deny[0] != "web_search" {
		t.Errorf("Deny = %v", sales.Tools.Deny)
	}
	if cfg.Agent("bad").ApprovalMode != "" {
		t.Errorf("an invalid mode must be dropped")
	}
	if got := cfg.AgentApprovalMode("sales"); got != ApprovalPlan {
		t.Errorf("AgentApprovalMode(sales) = %v", got)
	}
	if got := cfg.AgentApprovalMode("bad"); got != ApprovalEdits {
		t.Errorf("an invalid override falls back to the global mode, got %v", got)
	}
	if got := cfg.AgentApprovalMode(FinalAgentName); got != ApprovalEdits {
		t.Errorf("the default agent inherits the global mode, got %v", got)
	}
}

func TestNormaliseAgentGrounding(t *testing.T) {
	cfg := Default()
	cfg.Agents = map[string]Agent{
		"sales": {Grounding: " STRICT "},
		"bad":   {Grounding: "nonsense"},
	}
	cfg.normalise()
	if got := cfg.Agent("sales").Grounding; got != GroundingStrict {
		t.Errorf("sales grounding = %q", got)
	}
	if got := cfg.Agent("bad").Grounding; got != "" {
		t.Errorf("an invalid grounding must be dropped, got %q", got)
	}
	if got := cfg.AgentGrounding("sales"); got != GroundingStrict {
		t.Errorf("AgentGrounding(sales) = %q", got)
	}
	if got := cfg.AgentGrounding("bad"); got != GroundingNormal {
		t.Errorf("an invalid value reads as normal, got %q", got)
	}
}

func TestDelegationLimitsAndAutoApprove(t *testing.T) {
	cfg := Default()
	children, depth := cfg.DelegationLimits("default")
	if children != DefaultDelegationChildren || depth != 0 {
		t.Errorf("defaults = (%d, %d)", children, depth)
	}
	if !cfg.DelegationAutoApprove("default") {
		t.Errorf("an unset autoApprove means yes")
	}

	no := false
	cfg = cfg.WithAgent("sales", Agent{Delegation: Delegation{MaxChildren: 7, MaxDepth: 2, AutoApprove: &no}})
	children, depth = cfg.DelegationLimits("sales")
	if children != 7 || depth != 2 {
		t.Errorf("sales limits = (%d, %d)", children, depth)
	}
	if cfg.DelegationAutoApprove("sales") {
		t.Errorf("autoApprove=false must be honoured")
	}

	cfg = cfg.WithAgent("wide", Agent{Delegation: Delegation{MaxChildren: -1, MaxDepth: -1}})
	if children, depth := cfg.DelegationLimits("wide"); children != 0 || depth != 0 {
		t.Errorf("a negative value means no extra cap, got (%d, %d)", children, depth)
	}
}
