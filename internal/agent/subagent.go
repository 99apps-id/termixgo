package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/99apps-id/termixgo/internal/provider"
)

// SubagentMaxSteps is the default budget for a delegated investigation.
const SubagentMaxSteps = 12

// readOnlyTools are the tools a subagent may use: it can look, never touch.
func readOnlyTools() *Registry {
	return NewRegistry(
		&readFileTool{},
		&listDirectoryTool{},
		&grepTool{},
		&globTool{},
		&findSkillTool{},
		&useSkillTool{},
		&thinkTool{},
		// Git reads are useful for an investigation and change nothing.
		&gitStatusTool{},
		&gitDiffTool{},
		&gitLogTool{},
		&gitShowTool{},
	)
}

// RunReadOnlySubagent runs a nested, read-only investigation and returns its
// final answer. The nested run has its own session, so a large search cannot
// flood the parent context with file contents.
func RunReadOnlySubagent(ctx context.Context, parent *Env, client provider.Client, model, prompt string, maxSteps int) (string, error) {
	if client == nil {
		return "", fmt.Errorf("no provider client is available for a subagent")
	}
	if maxSteps <= 0 {
		maxSteps = SubagentMaxSteps
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
		// A subagent reports nothing: its result is the summary it returns.
		Emit: nil,
	}
	// A subagent may delegate once more. Without this the depth cap in the
	// subagent tool could never be reached, and the field would be dead
	// weight rather than a limit that actually holds.
	child.RunSubagent = func(childCtx context.Context, childPrompt string, readOnly bool) (string, error) {
		return RunReadOnlySubagent(childCtx, child, client, model, childPrompt, maxSteps)
	}
	runner := &Runner{
		Client:   client,
		Model:    model,
		Config:   parent.Config,
		Env:      child,
		Tools:    readOnlyTools(),
		Policy:   &ApprovalPolicy{Mode: ApprovalAll},
		MaxSteps: maxSteps,
	}
	session := NewSession(parent.Workspace, model)
	if err := runner.Run(ctx, session, prompt); err != nil {
		return "", err
	}
	answer := session.LastAssistantText()
	if strings.TrimSpace(answer) == "" {
		return "The subagent finished without a written answer.", nil
	}
	return answer, nil
}
