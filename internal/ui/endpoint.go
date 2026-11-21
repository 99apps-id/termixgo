package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// endpointModelsMsg carries a model refresh back to the event loop.
type endpointModelsMsg struct {
	id     string
	models []string
	err    error
}

// slashEndpoint manages OpenAI-compatible endpoints: several named servers
// (vLLM, LM Studio, LiteLLM, a company gateway) side by side, each with its
// own URL, key and models.
func (m *Model) slashEndpoint(args string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(args)
	action := "list"
	if len(fields) > 0 {
		action = strings.ToLower(fields[0])
	}
	notice := func(text string) (tea.Model, tea.Cmd) {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: text})
		m.refresh()
		return m, nil
	}
	switch action {
	case "list", "ls":
		endpoints := m.app.Endpoints()
		if len(endpoints) == 0 {
			return notice("No OpenAI-compatible endpoint yet. Add one: /endpoint add <name> <url> [key]")
		}
		lines := []string{"OpenAI-compatible endpoints:"}
		for _, endpoint := range endpoints {
			key := "no key"
			if endpoint.HasKey {
				key = "key set"
			}
			lines = append(lines, fmt.Sprintf("  %s  %s  (%s, %d model(s))", endpoint.ID, endpoint.URL, key, len(endpoint.Models)))
		}
		lines = append(lines, "Pick a model with /model <endpoint>:<model>. Refresh the list with /endpoint models <name>.")
		return notice(strings.Join(lines, "\n"))
	case "add", "set":
		if len(fields) < 3 {
			return notice("Usage: /endpoint add <name> <url> [key]")
		}
		key := ""
		if len(fields) > 3 {
			key = fields[3]
		}
		id, err := m.app.AddEndpoint(fields[1], fields[2], key)
		if err != nil {
			return notice("Endpoint: " + err.Error())
		}
		saved := "Saved " + id + "."
		if key != "" {
			saved += " The key is in the secret store."
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: saved + " Fetching its models..."})
		m.refresh()
		return m, m.fetchEndpointModels(id)
	case "rm", "remove", "delete":
		if len(fields) < 2 {
			return notice("Usage: /endpoint rm <name>")
		}
		id, err := m.app.RemoveEndpoint(fields[1])
		if err != nil {
			return notice("Endpoint: " + err.Error())
		}
		return notice("Removed " + id + " with its models and key.")
	case "models", "refresh":
		if len(fields) < 2 {
			return notice("Usage: /endpoint models <name>")
		}
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "Fetching models from " + fields[1] + "..."})
		m.refresh()
		return m, m.fetchEndpointModels(fields[1])
	default:
		return notice("Usage: /endpoint [list] | add <name> <url> [key] | rm <name> | models <name>")
	}
}

// fetchEndpointModels asks the endpoint for its models off the event loop.
func (m *Model) fetchEndpointModels(name string) tea.Cmd {
	application := m.app
	return func() tea.Msg {
		id, models, err := application.RefreshEndpointModels(context.Background(), name)
		return endpointModelsMsg{id: id, models: models, err: err}
	}
}

// applyEndpointModels reports a finished model refresh.
func (m *Model) applyEndpointModels(msg endpointModelsMsg) {
	if msg.err != nil {
		m.blocks = append(m.blocks, block{kind: blockError, text: fmt.Sprintf("%s models: %v. Add them by hand with /model %s:<model>.", msg.id, msg.err, msg.id)})
		m.refresh()
		return
	}
	shown := msg.models
	more := ""
	if len(shown) > 12 {
		more = fmt.Sprintf(" ... and %d more", len(shown)-12)
		shown = shown[:12]
	}
	m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("%s serves %d model(s): %s%s\nSwitch with /model %s:%s or Ctrl+L.", msg.id, len(msg.models), strings.Join(shown, ", "), more, msg.id, msg.models[0])})
	m.refresh()
}
