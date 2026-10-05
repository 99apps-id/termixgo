package provider

import "strings"

// The effort vocabulary is shared, but the wire field is not: each vendor
// spells it differently and one that accepts no control at all must be sent
// nothing. These are the shapes verified against the vendors' own docs:
//
//   - OpenAI: reasoning_effort at the top level of chat/completions.
//   - Anthropic: output_config.effort, no beta header.
//   - Gemini: generationConfig.thinkingConfig.thinkingLevel for Gemini 3,
//     thinkingBudget for 2.5.
//   - Antigravity: the same thinkingConfig on the Cloud Code envelope.
//   - xAI and DeepSeek: reasoning_effort on the OpenAI-compatible body.
//   - Moonshot and Zhipu: reasoning_effort, each with its own value set.
//
// A wrong value here is not a degraded request: these fields are strict, and
// the vendor answers 400, which loses the whole turn.
const (
	// effortContextDefault means the provider decides, which is what an empty
	// Effort asks for.
	effortContextDefault = "default"
)

// effortProviders are the OpenAI-compatible providers whose chat/completions
// body accepts a top-level reasoning_effort of low, medium and high.
//
// The list is deliberately explicit rather than "every OpenAI-compatible
// endpoint": a self-hosted or proxied endpoint that does not know the field
// rejects the request, and there is no way to discover that before sending it.
var effortProviders = map[string]bool{
	"openai":    true,
	"xai":       true,
	"xai-oauth": true,
	"deepseek":  true,
}

// openAIEffortValues maps the shared level onto the three values OpenAI and
// most compatible endpoints accept. max is not one of them, so it becomes the
// highest value that does work rather than an invalid field.
func openAIEffortValues(effort string) string {
	switch effort {
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh, EffortMax:
		return "high"
	}
	return ""
}

// openAIEffort is the value for a provider whose body carries reasoning_effort
// with the plain low/medium/high vocabulary.
func openAIEffort(providerID, effort string) string {
	if !effortProviders[strings.ToLower(strings.TrimSpace(providerID))] {
		return ""
	}
	return openAIEffortValues(effort)
}

// anthropicEffortValues are the levels the Anthropic effort parameter takes.
// All four map straight across, so max is passed on rather than folded.
func anthropicEffortValues(effort string) string {
	switch effort {
	case EffortLow, EffortMedium, EffortHigh, EffortMax:
		return effort
	}
	return ""
}

// googleEffort is the thinkingLevel for a Gemini 3 model. Every model in the
// catalogue that carries a thinking level is a Gemini 3, so no prefix test is
// needed: the registry is the gate.
func googleEffort(effort string) string {
	switch effort {
	case EffortLow, EffortMedium, EffortHigh:
		return effort
	case EffortMax:
		// Gemini 3 has no level above high, so its deepest thinking is high.
		return "high"
	}
	return ""
}

// googleThinkingBudget is the 2.5-series equivalent of a level. The documented
// range is 128 to 32768 for Pro and 0 to 24576 for Flash, so a level becomes a
// token count inside the range both models accept.
func googleThinkingBudget(effort string) int {
	switch effort {
	case EffortLow:
		return 1024
	case EffortMedium:
		return 8192
	case EffortHigh:
		return 24576
	case EffortMax:
		return 32768
	}
	return 0
}

// moonshotEffortValues are Moonshot's three levels. It has no medium, so the
// balanced level becomes the lower one: naming a level the model does not have
// would be worse than spending less than the operator asked for.
func moonshotEffortValues(effort string) string {
	switch effort {
	case EffortLow:
		return "low"
	case EffortMedium, EffortHigh:
		return "high"
	case EffortMax:
		return "max"
	}
	return ""
}

// zhipuEffortValues are Zhipu's three levels, with their own mapping: low and
// medium both land on high, and xhigh folds into max.
func zhipuEffortValues(effort string) string {
	switch effort {
	case EffortLow:
		return "low"
	case EffortMedium, EffortHigh:
		return "high"
	case EffortMax:
		return "max"
	}
	return ""
}

// compatibleEffort is the reasoning_effort value for an OpenAI-compatible
// body. It is the single entry point so the caller does not have to know which
// vendor spelling applies.
//
// The gate is per provider, not per model: the registry carries no per-model
// capability flag, and a model that does not take the field is better served by
// a provider the operator can leave unset than by a guess here.
func compatibleEffort(providerID, effort string) string {
	if value := openAIEffortValues(effort); value != "" && effortProviders[strings.ToLower(strings.TrimSpace(providerID))] {
		return value
	}
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "moonshot":
		return moonshotEffortValues(effort)
	case "zhipu":
		return zhipuEffortValues(effort)
	}
	return ""
}
