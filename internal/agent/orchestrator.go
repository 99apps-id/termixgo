package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// pipelinesDir is where a workspace keeps its orchestration pipelines. It is
// state the operator authors, not content the agent walks, so it sits under the
// same .termixgo directory as the journal and the search index.
const pipelinesDir = ".termixgo/pipelines"

// OrchestrationStep is one pipeline step, which runs as a subagent.
type OrchestrationStep struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Prompt      string   `json:"prompt"`
	Description string   `json:"description,omitempty"`
	DependsOn   []string `json:"depends_on,omitempty"`
	Parallel    bool     `json:"parallel,omitempty"`
}

// OrchestrationPipeline is a named set of steps.
type OrchestrationPipeline struct {
	ID          string              `json:"id"`
	Name        string              `json:"name,omitempty"`
	Description string              `json:"description,omitempty"`
	Steps       []OrchestrationStep `json:"steps"`
}

// OrchestrationResult is the outcome of one pipeline run.
type OrchestrationResult struct {
	PipelineID string         `json:"pipelineId"`
	Completed  []string       `json:"completed"`
	Failed     []string       `json:"failed"`
	Skipped    []string       `json:"skipped"`
	Results    map[string]any `json:"results"`
	StoppedAt  string         `json:"stoppedAt,omitempty"`
	// Spend is the delegated accounting of every step the pipeline ran. It is
	// not part of the JSON the model sees, so it travels to the tool result
	// silently and into the parent turn's /cost and cost budget.
	Spend SubagentSpend `json:"-"`
}

// pipelineDir returns the absolute pipelines directory for a workspace.
func pipelineDir(workspace string) string {
	return filepath.Join(workspace, filepath.FromSlash(pipelinesDir))
}

// loadPipeline reads one pipeline definition by id.
func loadPipeline(workspace, id string) (OrchestrationPipeline, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return OrchestrationPipeline{}, fmt.Errorf("pipeline id is required")
	}
	if strings.ContainsAny(trimmed, `/\`) || strings.Contains(trimmed, "..") {
		return OrchestrationPipeline{}, fmt.Errorf("invalid pipeline id %q", trimmed)
	}
	path := filepath.Join(pipelineDir(workspace), trimmed+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return OrchestrationPipeline{}, fmt.Errorf("pipeline %q not found in %s", trimmed, pipelinesDir)
	}
	var pipeline OrchestrationPipeline
	if err := json.Unmarshal(data, &pipeline); err != nil {
		return OrchestrationPipeline{}, fmt.Errorf("pipeline %q is not valid JSON: %v", trimmed, err)
	}
	if pipeline.ID == "" {
		pipeline.ID = trimmed
	}
	if pipeline.Name == "" {
		pipeline.Name = pipeline.ID
	}
	if len(pipeline.Steps) == 0 {
		return OrchestrationPipeline{}, fmt.Errorf("pipeline %q has no steps", trimmed)
	}
	for index, step := range pipeline.Steps {
		if strings.TrimSpace(step.ID) == "" {
			return OrchestrationPipeline{}, fmt.Errorf("pipeline %q step %d has no id", trimmed, index+1)
		}
		if strings.TrimSpace(step.Prompt) == "" {
			return OrchestrationPipeline{}, fmt.Errorf("pipeline %q step %q has no prompt", trimmed, step.ID)
		}
	}
	return pipeline, nil
}

// listPipelines reads every pipeline in the workspace.
func listPipelines(workspace string) []OrchestrationPipeline {
	entries, err := os.ReadDir(pipelineDir(workspace))
	if err != nil {
		return nil
	}
	var pipelines []OrchestrationPipeline
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		pipeline, err := loadPipeline(workspace, id)
		if err != nil {
			continue
		}
		pipelines = append(pipelines, pipeline)
	}
	sort.Slice(pipelines, func(i, j int) bool { return pipelines[i].ID < pipelines[j].ID })
	return pipelines
}

// stepPlaceholder matches {{step}}, {{step.field}} and {{ step.field }}.
var stepPlaceholder = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_-]+(?:\.[a-zA-Z0-9_-]+)?)\s*\}\}`)

// interpolatePrompt replaces step placeholders with values from prior results.
//
// A placeholder that names an unknown step is left literal rather than dropped,
// so the operator can see the typo instead of a silently empty prompt.
func interpolatePrompt(prompt string, context map[string]any) string {
	return stepPlaceholder.ReplaceAllStringFunc(prompt, func(match string) string {
		parts := stepPlaceholder.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		path := strings.SplitN(parts[1], ".", 2)
		value, ok := context[path[0]]
		if !ok {
			return match
		}
		if len(path) == 2 {
			field := path[1]
			if object, isObject := value.(map[string]any); isObject {
				if inner, present := object[field]; present {
					value = inner
				} else if field != "output" {
					return ""
				}
			} else if field != "output" {
				return ""
			}
		}
		return stringifyPlaceholder(value)
	})
}

func stringifyPlaceholder(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return toString(typed)
	default:
		encoded, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", value)
		}
		return string(encoded)
	}
}

// runPipeline executes steps in dependency order. A step whose dependency
// failed or was skipped is itself skipped. Parallel steps in the same ready
// batch run together; the rest run in order.
func runPipeline(ctx context.Context, env *Env, pipeline OrchestrationPipeline, initial map[string]any) OrchestrationResult {
	completed := map[string]bool{}
	failed := map[string]bool{}
	skipped := map[string]bool{}
	results := map[string]any{}
	// The whole pipeline's delegated spend is folded into one total, which
	// the orchestrate tool hands back so the parent turn charges it.
	spend := NewSubagentSpend()
	for key, value := range initial {
		results[key] = value
	}

	deps := make(map[string][]string, len(pipeline.Steps))
	byID := make(map[string]OrchestrationStep, len(pipeline.Steps))
	pending := make([]string, 0, len(pipeline.Steps))
	for _, step := range pipeline.Steps {
		if _, ok := byID[step.ID]; ok {
			return OrchestrationResult{PipelineID: pipeline.ID, Results: results, StoppedAt: step.ID, Failed: []string{step.ID}}
		}
		byID[step.ID] = step
		deps[step.ID] = step.DependsOn
		pending = append(pending, step.ID)
	}

	stoppedAt := ""
	for len(pending) > 0 {
		if ctx.Err() != nil {
			stoppedAt = "aborted"
			for _, id := range pending {
				skipped[id] = true
			}
			break
		}

		ready := make([]OrchestrationStep, 0, len(pending))
		var stillPending []string
		for _, id := range pending {
			dependencyMissing := false
			allMet := true
			for _, dep := range deps[id] {
				if failed[dep] || skipped[dep] {
					dependencyMissing = true
					break
				}
				if !completed[dep] {
					allMet = false
				}
			}
			switch {
			case dependencyMissing:
				skipped[id] = true
			case allMet:
				ready = append(ready, byID[id])
			default:
				stillPending = append(stillPending, id)
			}
		}
		pending = stillPending
		if len(ready) == 0 {
			for _, id := range pending {
				skipped[id] = true
			}
			break
		}

		parallel := make([]OrchestrationStep, 0, len(ready))
		sequential := make([]OrchestrationStep, 0, len(ready))
		for _, step := range ready {
			if step.Parallel {
				parallel = append(parallel, step)
				continue
			}
			sequential = append(sequential, step)
		}

		if len(parallel) > 0 {
			type outcome struct {
				id     string
				output string
				spend  SubagentSpend
				err    error
			}
			collected := make([]outcome, len(parallel))
			var wait sync.WaitGroup
			for index, step := range parallel {
				wait.Add(1)
				go func(index int, step OrchestrationStep) {
					defer wait.Done()
					snapshot := snapshotContext(results)
					output, stepSpend, err := runPipelineStep(ctx, env, step, snapshot)
					collected[index] = outcome{id: step.ID, output: output, spend: stepSpend, err: err}
				}(index, step)
			}
			wait.Wait()
			for _, item := range collected {
				spend.Fold(item.spend)
				if item.err == nil {
					completed[item.id] = true
					results[item.id] = item.output
					continue
				}
				failed[item.id] = true
				results[item.id] = map[string]any{"error": item.err.Error()}
				if stoppedAt == "" {
					stoppedAt = item.id
				}
			}
		}

		if stoppedAt != "" || ctx.Err() != nil {
			if stoppedAt == "" {
				stoppedAt = "aborted"
			}
			for _, id := range pending {
				skipped[id] = true
			}
			break
		}

		for _, step := range sequential {
			if ctx.Err() != nil {
				stoppedAt = "aborted"
				break
			}
			output, stepSpend, err := runPipelineStep(ctx, env, step, snapshotContext(results))
			if err != nil {
				spend.Fold(stepSpend)
				failed[step.ID] = true
				results[step.ID] = map[string]any{"error": err.Error()}
				stoppedAt = step.ID
				for _, id := range pending {
					skipped[id] = true
				}
				break
			}
			spend.Fold(stepSpend)
			completed[step.ID] = true
			results[step.ID] = output
		}
	}

	result := OrchestrationResult{
		PipelineID: pipeline.ID,
		Completed:  sortedKeys(completed),
		Failed:     sortedKeys(failed),
		Skipped:    sortedKeys(skipped),
		Results:    results,
		StoppedAt:  stoppedAt,
		Spend:      spend,
	}
	return result
}

// runPipelineStep runs one step as a subagent.
func runPipelineStep(ctx context.Context, env *Env, step OrchestrationStep, context map[string]any) (string, SubagentSpend, error) {
	if env == nil || env.RunSubagent == nil {
		return "", SubagentSpend{}, fmt.Errorf("subagents are not available in this session; run inside a normal turn with subagents enabled")
	}
	prompt := interpolatePrompt(step.Prompt, context)
	subType := strings.TrimSpace(step.Type)
	if subType == "" {
		subType = string(SubagentGeneral)
	}
	report, spend, err := env.RunSubagent(ctx, subType, prompt)
	if err != nil {
		return "", spend, err
	}
	return report, spend, nil
}

// snapshotContext copies the results map so parallel steps read a stable view.
func snapshotContext(results map[string]any) map[string]any {
	snapshot := make(map[string]any, len(results))
	for key, value := range results {
		snapshot[key] = value
	}
	return snapshot
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
