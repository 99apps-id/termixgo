package app

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/99apps-id/termixgo/internal/agent"
)

// RunIsolatedTurn runs one prompt on a throwaway session for another surface
// (the HTTP API) and returns only what that turn produced. It never touches the
// operator's conversation, and it enters the queue as a channel caller: below
// the terminal, above a scheduled job.
func (a *App) RunIsolatedTurn(ctx context.Context, prompt, agentName string, maxSteps int) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", errors.New("prompt is required")
	}
	agentName = strings.TrimSpace(agentName)
	if agentName == "" || !a.Config().HasAgent(agentName) {
		agentName = a.ActiveAgent()
	}
	session := agent.NewSessionFor(agentName, a.AgentWorkspace(agentName), a.CurrentModel().ID)
	if err := a.runWithOptions(ctx, prompt, runOptions{
		session:   session,
		isolated:  true,
		agentName: agentName,
		maxSteps:  maxSteps,
		priority:  PriorityChannel,
	}); err != nil {
		return "", err
	}
	return session.LastAssistantText(), nil
}

// ModelID reports the active model id.
func (a *App) ModelID() string { return a.CurrentModel().ID }

// ModelProvider reports the active model's provider id.
func (a *App) ModelProvider() string { return a.CurrentModel().Provider }

// QueueStatus describes the run slot for the HTTP API and /status.
func (a *App) QueueStatus() string { return a.runQueueStatus() }

// ToolNames lists the tools the active registry offers, sorted, which is what
// an integrating program reads to know what this agent can do.
func (a *App) ToolNames() []string {
	registry := a.Tools()
	if registry == nil {
		return nil
	}
	tools := registry.Tools()
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name())
	}
	sort.Strings(names)
	return names
}
