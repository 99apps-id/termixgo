package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// listWorkflowsTool lists available workflow names.
type listWorkflowsTool struct{}

func (t *listWorkflowsTool) Name() string      { return "list_workflows" }
func (t *listWorkflowsTool) Aliases() []string { return []string{"workflows"} }
func (t *listWorkflowsTool) Mutating() bool    { return false }
func (t *listWorkflowsTool) Risk() Risk        { return RiskEdit }
func (t *listWorkflowsTool) Description() string {
	return "List saved workflow names."
}
func (t *listWorkflowsTool) Schema() map[string]any {
	return object(map[string]any{})
}
func (t *listWorkflowsTool) Label(_ map[string]any) string     { return "Listing workflows" }
func (t *listWorkflowsTool) DoneLabel(_ map[string]any) string { return "Listed workflows" }

func (t *listWorkflowsTool) Run(_ context.Context, _ *Env, _ map[string]any) (Result, error) {
	names, err := listWorkflows()
	if err != nil {
		return Result{Output: fmt.Sprintf("list failed: %v", err), IsError: true}, nil
	}
	if len(names) == 0 {
		return Result{Output: "No workflows saved."}, nil
	}
	return Result{Output: strings.Join(names, "\n")}, nil
}

// runWorkflowTool runs a workflow by name.
type runWorkflowTool struct{}

func (t *runWorkflowTool) Name() string      { return "run_workflow" }
func (t *runWorkflowTool) Aliases() []string { return []string{"execute_workflow"} }
func (t *runWorkflowTool) Mutating() bool    { return true }
func (t *runWorkflowTool) Risk() Risk        { return RiskCommand }
func (t *runWorkflowTool) Description() string {
	return "Run a saved workflow by name."
}
func (t *runWorkflowTool) Schema() map[string]any {
	return object(map[string]any{
		"name":    strProp("Workflow name."),
		"context": strProp("Optional initial context as JSON."),
	})
}
func (t *runWorkflowTool) Label(a map[string]any) string {
	return "Running workflow " + Shorten(argString(a, "name"), 30)
}
func (t *runWorkflowTool) DoneLabel(a map[string]any) string {
	return "Ran workflow " + Shorten(argString(a, "name"), 30)
}

func (t *runWorkflowTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	name := argString(args, "name")
	if name == "" {
		return Result{Output: "name is required", IsError: true}, nil
	}
	wf, err := loadWorkflow(name)
	if err != nil {
		return Result{Output: fmt.Sprintf("load failed: %v", err), IsError: true}, nil
	}

	initial := make(map[string]string)
	if ctx := argString(args, "context"); ctx != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(ctx), &parsed); err == nil {
			for k, v := range parsed {
				initial[k] = fmt.Sprintf("%v", v)
			}
		}
	}

	res, err := runWorkflow(wf, nil, initial)
	if err != nil {
		return Result{Output: fmt.Sprintf("run failed: %v", err), IsError: true}, nil
	}

	data, _ := json.MarshalIndent(res, "", "  ")
	return Result{Output: string(data)}, nil
}
