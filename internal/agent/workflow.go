package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// workflowStep is a single step in a workflow.
type workflowStep struct {
	ID        string         `json:"id"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args"`
	DependsOn []string       `json:"depends_on,omitempty"`
}

// workflow is a JSON-defined multi-step workflow.
type workflow struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	TimeoutSecs int            `json:"timeout_seconds,omitempty"`
	Steps       []workflowStep `json:"steps"`
}

// workflowResult captures the outcome of a workflow run.
type workflowResult struct {
	Workflow  string
	Status    string
	Results   map[string]string
	StoppedAt string
}

var (
	workflowsDir     string
	workflowsDirOnce sync.Once
)

// workflowsDirPath returns the workflows directory, creating it if needed.
func workflowsDirPath() (string, error) {
	workflowsDirOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		workflowsDir = filepath.Join(home, ".termixgo", "workflows")
		_ = os.MkdirAll(workflowsDir, 0o700)
	})
	return workflowsDir, nil
}

// listWorkflows returns workflow names found on disk.
func listWorkflows() ([]string, error) {
	dir, err := workflowsDirPath()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	return names, nil
}

// loadWorkflow loads a workflow by name from disk.
func loadWorkflow(name string) (*workflow, error) {
	dir, err := workflowsDirPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil {
		return nil, err
	}
	var wf workflow
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	if wf.Name == "" {
		wf.Name = name
	}
	return &wf, nil
}

// runWorkflow executes a workflow DAG using shell commands for each step.
func runWorkflow(wf *workflow, env *Env, initial map[string]string) (*workflowResult, error) {
	result := &workflowResult{
		Workflow: wf.Name,
		Results:  initial,
	}
	if result.Results == nil {
		result.Results = make(map[string]string)
	}

	timeout := 30 * time.Second
	if wf.TimeoutSecs > 0 {
		timeout = time.Duration(wf.TimeoutSecs) * time.Second
	}
	deadline := time.Now().Add(timeout)

	pending := make(map[string]workflowStep)
	completed := make(map[string]string)
	failed := make(map[string]string)

	for _, step := range wf.Steps {
		pending[step.ID] = step
	}

	for len(pending) > 0 {
		if time.Now().After(deadline) {
			result.Status = "timed_out"
			break
		}

		ready := []workflowStep{}
		for _, step := range pending {
			deps := step.DependsOn
			ok := true
			for _, dep := range deps {
				if _, done := completed[dep]; !done {
					if _, fail := failed[dep]; !fail {
						ok = false
						break
					}
				}
			}
			if ok {
				ready = append(ready, step)
			}
		}

		if len(ready) == 0 {
			result.Status = "deadlock"
			for id := range pending {
				result.StoppedAt = id
				break
			}
			break
		}

		for _, step := range ready {
			if time.Now().After(deadline) {
				result.Status = "timed_out"
				break
			}

			// Execute step as shell command.
			command := step.Tool
			if step.Args != nil {
				if cmd, ok := step.Args["command"].(string); ok {
					command = cmd
				}
			}

			runCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			cmd := exec.CommandContext(runCtx, "bash", "-c", command)
			if env != nil && env.Workspace != "" {
				cmd.Dir = env.Workspace
			}
			out, err := cmd.CombinedOutput()
			cancel()

			delete(pending, step.ID)
			output := strings.TrimRight(string(out), "\n")
			if err != nil {
				failed[step.ID] = fmt.Sprintf("command error: %v: %s", err, output)
				result.Status = "failed"
			} else {
				completed[step.ID] = output
				result.Results[step.ID] = output
			}
		}
	}

	if result.Status == "" {
		if len(failed) > 0 {
			result.Status = "failed"
		} else {
			result.Status = "completed"
		}
	}

	return result, nil
}
