package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// orchestrateTool runs a multi-step pipeline of subagents.
type orchestrateTool struct{}

func (t *orchestrateTool) Name() string      { return "orchestrate" }
func (t *orchestrateTool) Aliases() []string { return []string{"run_pipeline"} }
func (t *orchestrateTool) Mutating() bool    { return false }
func (t *orchestrateTool) Risk() Risk        { return RiskEdit }
func (t *orchestrateTool) Label(a map[string]any) string {
	return "Orchestrating " + Shorten(argString(a, "pipeline_id"), 40)
}
func (t *orchestrateTool) DoneLabel(a map[string]any) string {
	return "Orchestrated " + Shorten(argString(a, "pipeline_id"), 40)
}
func (t *orchestrateTool) Description() string {
	return "Run a multi-agent orchestration pipeline defined in .termixgo/pipelines/<id>.json. Each step runs as a subagent in dependency order; parallel steps run together. Use list_pipelines to discover the ids."
}
func (t *orchestrateTool) Schema() map[string]any {
	return object(map[string]any{
		"pipeline_id": strProp("Pipeline id, the file name without .json."),
		"context":     map[string]any{"type": "object", "description": "Optional initial values for {{step}} placeholders."},
	}, "pipeline_id")
}

func (t *orchestrateTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	id := strings.TrimSpace(argString(args, "pipeline_id"))
	if id == "" {
		return Result{Output: "pipeline_id is required", IsError: true}, nil
	}
	if env == nil || env.RunSubagent == nil {
		return Result{Output: "Subagents are not available in this session.", IsError: true}, nil
	}
	pipeline, err := loadPipeline(env.Workspace, id)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	initial := map[string]any{}
	if raw, ok := args["context"].(map[string]any); ok {
		initial = raw
	}
	result := runPipeline(ctx, env, pipeline, initial)

	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return Result{Output: fmt.Sprintf("pipeline %s finished: %d completed, %d failed, %d skipped", id, len(result.Completed), len(result.Failed), len(result.Skipped))}, nil
	}
	output := string(encoded)
	if len(output) > 16000 {
		output = clipBytes(output, 16000) + "\n... [truncated]"
	}
	return Result{
		Output:            output,
		IsError:           len(result.Failed) > 0,
		SubagentUsage:     result.Spend.Usage,
		SubagentCost:      result.Spend.Cost,
		SubagentCostKnown: result.Spend.CostKnown,
		SubagentUnpriced:  result.Spend.Unpriced,
		SubagentSpent:     true,
	}, nil
}

// listPipelinesTool names the pipelines an operator has authored.
type listPipelinesTool struct{}

func (t *listPipelinesTool) Name() string      { return "list_pipelines" }
func (t *listPipelinesTool) Aliases() []string { return []string{"pipelines"} }
func (t *listPipelinesTool) Mutating() bool    { return false }
func (t *listPipelinesTool) Risk() Risk        { return RiskEdit }
func (t *listPipelinesTool) Label(a map[string]any) string {
	return "Listing pipelines"
}
func (t *listPipelinesTool) DoneLabel(a map[string]any) string {
	return "Listed pipelines"
}
func (t *listPipelinesTool) Description() string {
	return "List the orchestration pipelines defined in .termixgo/pipelines/. Read-only."
}
func (t *listPipelinesTool) Schema() map[string]any {
	return object(map[string]any{})
}

func (t *listPipelinesTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	pipelines := listPipelines(env.Workspace)
	if len(pipelines) == 0 {
		return Result{Output: "No pipelines found. Add one at " + pipelinesDir + "/<id>.json."}, nil
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "%d pipeline(s)", len(pipelines))
	for _, pipeline := range pipelines {
		fmt.Fprintf(&builder, "\n- %s: %s (%d step(s))", pipeline.ID, pipeline.Name, len(pipeline.Steps))
		if strings.TrimSpace(pipeline.Description) != "" {
			builder.WriteString(" - " + pipeline.Description)
		}
	}
	return Result{Output: builder.String()}, nil
}
