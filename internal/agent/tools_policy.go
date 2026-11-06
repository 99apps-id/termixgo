package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// policyStore holds operator-defined guardrails.
type policyStore struct {
	mu       sync.Mutex
	policies []string
	path     string
}

var sharedPolicies = &policyStore{}

func (s *policyStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		s.path = filepath.Join(home, ".termixgo", "policies.txt")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.policies = nil
			return nil
		}
		return err
	}
	s.policies = strings.Split(string(data), "\n")
	var clean []string
	for _, p := range s.policies {
		p = strings.TrimSpace(p)
		if p != "" {
			clean = append(clean, p)
		}
	}
	s.policies = clean
	return nil
}

func (s *policyStore) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return fmt.Errorf("policy store not initialized")
	}
	return os.WriteFile(s.path, []byte(strings.Join(s.policies, "\n")+"\n"), 0o600)
}

// listPoliciesTool lists saved policies.
type listPoliciesTool struct{}

func (t *listPoliciesTool) Name() string      { return "list_policies" }
func (t *listPoliciesTool) Aliases() []string { return []string{"policies"} }
func (t *listPoliciesTool) Mutating() bool    { return false }
func (t *listPoliciesTool) Risk() Risk        { return RiskEdit }
func (t *listPoliciesTool) Description() string {
	return "List operator-defined policies."
}
func (t *listPoliciesTool) Schema() map[string]any {
	return object(map[string]any{})
}
func (t *listPoliciesTool) Label(_ map[string]any) string     { return "Listing policies" }
func (t *listPoliciesTool) DoneLabel(_ map[string]any) string { return "Listed policies" }

func (t *listPoliciesTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	if err := sharedPolicies.load(); err != nil {
		return Result{Output: fmt.Sprintf("load failed: %v", err), IsError: true}, nil
	}
	if len(sharedPolicies.policies) == 0 {
		return Result{Output: "No policies defined."}, nil
	}
	return Result{Output: strings.Join(sharedPolicies.policies, "\n")}, nil
}

// pinInvariantTool adds a new invariant.
type pinInvariantTool struct{}

func (t *pinInvariantTool) Name() string      { return "pin_invariant" }
func (t *pinInvariantTool) Aliases() []string { return []string{"add_invariant", "invariant"} }
func (t *pinInvariantTool) Mutating() bool    { return true }
func (t *pinInvariantTool) Risk() Risk        { return RiskEdit }
func (t *pinInvariantTool) Description() string {
	return "Add an invariant rule that must hold across edits."
}
func (t *pinInvariantTool) Schema() map[string]any {
	return object(map[string]any{
		"rule": strProp("Invariant rule text, e.g., 'all DB queries must use parameterized statements'."),
	})
}
func (t *pinInvariantTool) Label(a map[string]any) string {
	return "Pinning invariant " + Shorten(argString(a, "rule"), 40)
}
func (t *pinInvariantTool) DoneLabel(a map[string]any) string {
	return "Pinned invariant " + Shorten(argString(a, "rule"), 40)
}

func (t *pinInvariantTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	rule := strings.TrimSpace(argString(args, "rule"))
	if rule == "" {
		return Result{Output: "rule is required", IsError: true}, nil
	}
	if err := sharedPolicies.load(); err != nil {
		return Result{Output: fmt.Sprintf("load failed: %v", err), IsError: true}, nil
	}
	sharedPolicies.mu.Lock()
	sharedPolicies.policies = append(sharedPolicies.policies, rule)
	sharedPolicies.mu.Unlock()
	if err := sharedPolicies.save(); err != nil {
		return Result{Output: fmt.Sprintf("save failed: %v", err), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Pinned invariant: %s", rule)}, nil
}

// listInvariantsTool lists invariants.
type listInvariantsTool struct{}

func (t *listInvariantsTool) Name() string      { return "list_invariants" }
func (t *listInvariantsTool) Aliases() []string { return []string{"invariants"} }
func (t *listInvariantsTool) Mutating() bool    { return false }
func (t *listInvariantsTool) Risk() Risk        { return RiskEdit }
func (t *listInvariantsTool) Description() string {
	return "List pinned invariant rules."
}
func (t *listInvariantsTool) Schema() map[string]any {
	return object(map[string]any{})
}
func (t *listInvariantsTool) Label(_ map[string]any) string     { return "Listing invariants" }
func (t *listInvariantsTool) DoneLabel(_ map[string]any) string { return "Listed invariants" }

func (t *listInvariantsTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	if err := sharedPolicies.load(); err != nil {
		return Result{Output: fmt.Sprintf("load failed: %v", err), IsError: true}, nil
	}
	if len(sharedPolicies.policies) == 0 {
		return Result{Output: "No invariants pinned."}, nil
	}
	return Result{Output: strings.Join(sharedPolicies.policies, "\n")}, nil
}
