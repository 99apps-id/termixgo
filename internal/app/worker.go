package app

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
)

// WorkerInfo describes one background coding worker for a listing.
type WorkerInfo struct {
	Kind      string
	Available bool
	Detail    string
}

// WorkerCatalog lists the supported workers and whether each can run. The
// native worker is always available; an external CLI must be on PATH or have a
// configured command template.
func (a *App) WorkerCatalog() []WorkerInfo {
	cfg := a.Config()
	infos := make([]WorkerInfo, 0, 4)
	for _, kind := range agent.WorkerKinds() {
		info := WorkerInfo{Kind: kind}
		if kind == "termixgo" {
			info.Available = true
			info.Detail = "this binary, using the configured model"
			infos = append(infos, info)
			continue
		}
		if template, ok := cfg.WorkerCommands[kind]; ok && len(template) > 0 {
			if _, err := exec.LookPath(template[0]); err == nil {
				info.Available = true
				info.Detail = "configured: " + strings.Join(template, " ")
			} else {
				info.Detail = template[0] + " is not on PATH"
			}
			infos = append(infos, info)
			continue
		}
		if _, err := exec.LookPath(kind); err == nil {
			info.Available = true
			info.Detail = "found " + kind + " on PATH"
		} else {
			info.Detail = kind + " is not installed"
		}
		infos = append(infos, info)
	}
	return infos
}

// StartCodeWorker starts a background coding worker and returns its handle. It
// runs the code_worker tool directly, because an operator-issued command is its
// own approval.
func (a *App) StartCodeWorker(ctx context.Context, kind, task, name string) (string, error) {
	tool, ok := a.tools.Lookup("code_worker")
	if !ok {
		return "", errors.New("the code worker tool is unavailable")
	}
	args := map[string]any{"task": task}
	if strings.TrimSpace(kind) != "" {
		args["worker"] = kind
	}
	if strings.TrimSpace(name) != "" {
		args["name"] = name
	}
	result, err := tool.Run(ctx, a.env(), args)
	if err != nil {
		return "", err
	}
	if result.IsError {
		return "", errors.New(result.Output)
	}
	return result.Output, nil
}

// StartParallelBatch fans tasks out to one worker per worktree.
func (a *App) StartParallelBatch(ctx context.Context, tasks []string, kind string) (string, error) {
	tool, ok := a.tools.Lookup("parallel_batch")
	if !ok {
		return "", errors.New("the parallel batch tool is unavailable")
	}
	raw := make([]any, 0, len(tasks))
	for _, task := range tasks {
		if strings.TrimSpace(task) == "" {
			continue
		}
		raw = append(raw, map[string]any{"task": task})
	}
	args := map[string]any{"tasks": raw}
	if strings.TrimSpace(kind) != "" {
		args["worker"] = kind
	}
	result, err := tool.Run(ctx, a.env(), args)
	if err != nil {
		return "", err
	}
	if result.IsError {
		return "", errors.New(result.Output)
	}
	return result.Output, nil
}

// BatchStatus reports every tracked parallel worktree with its live worker
// state. It joins the worktree registry with the process table so one screen
// answers which checkout is running, done, failed or idle.
func (a *App) BatchStatus() string {
	return agent.BatchStatusFor(a.workspace, a.processes, time.Now())
}
