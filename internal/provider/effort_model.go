package provider

import (
	"os"
	"strings"
)

// effortOrder is the shared vocabulary in escalation order. A level that a
// model does not offer is lowered to the highest one it does, never sent: the
// effort fields are strict and an unknown value answers 400, which loses the
// whole turn.
var effortOrder = []string{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortUltra}

func effortRank(effort string) int {
	for index, level := range effortOrder {
		if level == effort {
			return index
		}
	}
	return -1
}

// levelsUpTo returns the shared levels from low through top, inclusive.
func levelsUpTo(top string) []string {
	rank := effortRank(top)
	if rank < 0 {
		return nil
	}
	return effortOrder[:rank+1]
}

// clampEffort lowers effort to the highest level in supported that does not
// exceed it, and raises it to the lowest supported level when the request is
// below all of them. An empty or unknown effort stays empty, which means the
// vendor default.
func clampEffort(effort string, supported []string) string {
	rank := effortRank(effort)
	if rank < 0 || len(supported) == 0 {
		return ""
	}
	best := ""
	bestRank := -1
	lowest := ""
	lowestRank := len(effortOrder)
	for _, level := range supported {
		levelRank := effortRank(level)
		if levelRank < 0 {
			continue
		}
		if levelRank <= rank && levelRank > bestRank {
			best, bestRank = level, levelRank
		}
		if levelRank < lowestRank {
			lowest, lowestRank = level, levelRank
		}
	}
	if best != "" {
		return best
	}
	return lowest
}

// normalizedModelID lowercases a model id and folds the dot spelling
// (claude-sonnet-5.5) into the dash one, so the tables below match either.
func normalizedModelID(model string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(model)), ".", "-")
}

// codexEffortLevels is what the ChatGPT backend's own model catalogue lists as
// supported_reasoning_levels. ultra is the level that adds automatic task
// delegation, so it is offered only to the models that list it. A model the
// catalogue does not know keeps the conservative set.
func codexEffortLevels(model string) []string {
	m := normalizedModelID(model)
	switch {
	case strings.Contains(m, "gpt-6-2-"), strings.Contains(m, "gpt-6-1-"),
		strings.Contains(m, "gpt-6-sol"), strings.Contains(m, "gpt-6-astra"),
		strings.Contains(m, "gpt-5-6-sol"), strings.Contains(m, "gpt-5-6-terra"):
		return levelsUpTo(EffortUltra)
	case strings.Contains(m, "gpt-6"), strings.Contains(m, "gpt-5-6"),
		strings.Contains(m, "gpt-reserve"), strings.Contains(m, "codex-auto-review"):
		return levelsUpTo(EffortMax)
	case strings.Contains(m, "gpt-5-5"), strings.Contains(m, "gpt-5-4"),
		strings.Contains(m, "gpt-5-3"), strings.Contains(m, "gpt-5-2"),
		strings.Contains(m, "codex-max"):
		return levelsUpTo(EffortXHigh)
	}
	return levelsUpTo(EffortHigh)
}

// codexEffort is the reasoning.effort value for a Codex model.
func codexEffort(model, effort string) string {
	return clampEffort(effort, codexEffortLevels(model))
}

// anthropicEffortLevels is what a Claude model accepts in output_config.effort,
// read from Claude Code's own model table: the 4.7 line and everything from
// 5 up take all five levels, the 4.6 line stops at max below xhigh, and older
// models take the original three.
func anthropicEffortLevels(model string) []string {
	m := normalizedModelID(model)
	switch {
	case strings.Contains(m, "fable"), strings.Contains(m, "mythos"),
		strings.Contains(m, "opus-5"), strings.Contains(m, "sonnet-5"), strings.Contains(m, "haiku-5"),
		strings.Contains(m, "opus-4-7"), strings.Contains(m, "opus-4-8"):
		return levelsUpTo(EffortMax)
	case strings.Contains(m, "opus-4-6"), strings.Contains(m, "sonnet-4-6"):
		return []string{EffortLow, EffortMedium, EffortHigh, EffortMax}
	}
	return levelsUpTo(EffortHigh)
}

// anthropicEffort is the output_config.effort value for a Claude model.
func anthropicEffort(model, effort string) string {
	return clampEffort(anthropicEffortValues(effort), anthropicEffortLevels(model))
}

// copilotEffortLevels is what GitHub Copilot's /models lists as
// capabilities.supports.reasoning_effort for the chat models. A model not
// named here gets the three levels every OpenAI-compatible endpoint takes.
func copilotEffortLevels(model string) []string {
	m := normalizedModelID(model)
	switch {
	case strings.Contains(m, "kimi"):
		return []string{EffortLow, EffortHigh, EffortMax}
	case strings.Contains(m, "gemini-4-hydrogen"):
		return levelsUpTo(EffortXHigh)
	case strings.Contains(m, "gemini"), strings.Contains(m, "mai-code-1"):
		return levelsUpTo(EffortHigh)
	case strings.Contains(m, "grok"), strings.Contains(m, "gpt-5-3"):
		return levelsUpTo(EffortXHigh)
	case strings.Contains(m, "gpt-6"), strings.Contains(m, "gpt-5-6"), strings.Contains(m, "mai-code-2"):
		return levelsUpTo(EffortMax)
	}
	return levelsUpTo(EffortHigh)
}

// compatibleEffortFor is compatibleEffort with the model in hand, for the
// providers whose supported levels differ per model.
func compatibleEffortFor(providerID, model, effort string) string {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "openai-codex":
		return codexEffort(model, effort)
	case "github-copilot":
		return clampEffort(effort, copilotEffortLevels(model))
	}
	return compatibleEffort(providerID, effort)
}

// anthropicAdaptiveThinking reports whether a Claude model takes
// thinking: {type: adaptive}. It is the set that also takes the full effort
// range, which is the set Claude Code's own model table marks adaptive.
func anthropicAdaptiveThinking(model string) bool {
	return len(anthropicEffortLevels(model)) == len(levelsUpTo(EffortMax))
}

// claudeThinkingEnabled is the opt-out: TERMIXGO_CLAUDE_THINKING=off sends no
// thinking parameter, for an account or model that rejects it.
func claudeThinkingEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("TERMIXGO_CLAUDE_THINKING")), "off")
}
