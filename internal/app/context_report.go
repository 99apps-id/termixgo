package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/provider"
)

// ContextReport breaks the next request down by part, so the operator can see
// what fills the window: the system prompt, the learned memory, the tool
// schemas and the history by role, with the largest tool results named.
// Figures are estimates at four characters a token, except the measured total
// when a step has reported one.
func (a *App) ContextReport() string {
	session := a.Session()
	model := a.CurrentModel()
	env := a.env()
	staticPrompt, _ := agent.BuildSystemParts(env, model.ID)
	memory := ""
	if env.Memory != nil {
		memory = env.Memory.PromptBlock()
	}
	systemTokens := agent.EstimateTokens(staticPrompt) - agent.EstimateTokens(memory)
	memoryTokens := agent.EstimateTokens(memory)
	toolTokens := 0
	toolCount := 0
	toolSearch := false
	if a.tools != nil {
		toolSearch = a.Config().ToolSearchEnabled || shouldDeferTools(len(a.tools.Tools()))
		definitions := agent.AdvertisedTools(a.tools, toolSearch, session.DiscoveredTools()).Definitions()
		toolCount = len(definitions)
		toolTokens = agent.EstimateToolSchemas(definitions)
	}

	type big struct {
		name   string
		tokens int
	}
	var userTokens, assistantTokens, toolResultTokens, userCount, assistantCount, toolResultCount int
	var biggest []big
	for _, message := range session.Messages() {
		tokens := agent.EstimateMessages([]provider.Message{message})
		switch message.Role {
		case provider.RoleUser:
			userTokens += tokens
			userCount++
		case provider.RoleAssistant:
			assistantTokens += tokens
			assistantCount++
		case provider.RoleTool:
			toolResultTokens += tokens
			toolResultCount++
			biggest = append(biggest, big{name: message.Name, tokens: tokens})
		}
	}
	sort.SliceStable(biggest, func(i, j int) bool { return biggest[i].tokens > biggest[j].tokens })
	if len(biggest) > 5 {
		biggest = biggest[:5]
	}

	window := model.Window()
	total := systemTokens + memoryTokens + toolTokens + userTokens + assistantTokens + toolResultTokens
	percent := func(tokens int) string {
		if window <= 0 {
			return ""
		}
		return fmt.Sprintf(" (%.1f%%)", float64(tokens)*100/float64(window))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Context for %s, window %s tokens\n", firstNonEmptyString(model.Label, model.ID), thousands(window))
	fmt.Fprintf(&b, "  system prompt     %8s%s\n", thousands(systemTokens), percent(systemTokens))
	fmt.Fprintf(&b, "  learned memory    %8s%s\n", thousands(memoryTokens), percent(memoryTokens))
	toolNote := ""
	if toolSearch {
		toolNote = "  (tool search: the always-on set plus tools loaded this session)"
	}
	fmt.Fprintf(&b, "  tool schemas      %8s%s  %d tools%s\n", thousands(toolTokens), percent(toolTokens), toolCount, toolNote)
	fmt.Fprintf(&b, "  your messages     %8s%s  %d\n", thousands(userTokens), percent(userTokens), userCount)
	fmt.Fprintf(&b, "  answers and calls %8s%s  %d\n", thousands(assistantTokens), percent(assistantTokens), assistantCount)
	fmt.Fprintf(&b, "  tool results      %8s%s  %d\n", thousands(toolResultTokens), percent(toolResultTokens), toolResultCount)
	fmt.Fprintf(&b, "  estimated total   %8s%s\n", thousands(total), percent(total))
	a.mu.Lock()
	measured := a.measured
	a.mu.Unlock()
	if measured.tokens > 0 && measured.sessionID == session.ID() && measured.modelID == model.ID {
		fmt.Fprintf(&b, "  last step, as the provider counted it: %s prompt tokens\n", thousands(measured.tokens))
	}
	if len(biggest) > 0 {
		b.WriteString("Largest tool results:\n")
		for _, item := range biggest {
			fmt.Fprintf(&b, "  %-24s %8s\n", firstNonEmptyString(item.name, "tool"), thousands(item.tokens))
		}
	}
	b.WriteString("/compact condenses the history now; /new starts empty.")
	return b.String()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// thousands formats a count with comma separators.
func thousands(value int) string {
	text := fmt.Sprintf("%d", value)
	if value < 0 {
		return text
	}
	var out []byte
	for index := range text {
		if index > 0 && (len(text)-index)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, text[index])
	}
	return string(out)
}
