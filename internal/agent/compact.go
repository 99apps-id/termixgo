package agent

import (
	"fmt"
	"strconv"

	"github.com/99apps-id/termixgo/internal/provider"
)

// charsPerToken deliberately underestimates the text per token, which
// over-estimates the token count: running out of window is worse than
// trimming slightly early.
const charsPerToken = 2.6

// Compact tuning: the newest turns are kept whole, older ones are elided.
const (
	keepTailMessages = 16
	keepMinMessages  = 4
	elidedToolBody   = "[elided: earlier tool output]"
	elidedTextLimit  = 600
)

// EstimateTokens approximates the token count of a string.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return int(float64(len(text))/charsPerToken) + 1
}

// estimateMessageTokens approximates one message's cost.
func estimateMessageTokens(message provider.Message) int {
	total := EstimateTokens(message.Content) + EstimateTokens(message.Reasoning)
	for _, call := range message.ToolCalls {
		total += EstimateTokens(call.Name) + EstimateTokens(call.Arguments)
	}
	// Per-message overhead the provider adds for role and framing.
	return total + 8
}

// EstimateMessages sums the token estimate for a conversation.
func EstimateMessages(messages []provider.Message) int {
	total := 0
	for _, message := range messages {
		total += estimateMessageTokens(message)
	}
	return total
}

// Compact trims a conversation to fit a token budget. The newest messages are
// kept intact; older tool output and text are replaced by short markers. The
// returned slice always starts with a user or assistant turn, never a tool
// result, because a leading tool result is an invalid sequence.
func Compact(messages []provider.Message, budgetTokens int) []provider.Message {
	if budgetTokens <= 0 || EstimateMessages(messages) <= budgetTokens {
		return messages
	}
	trimmed := make([]provider.Message, len(messages))
	copy(trimmed, messages)

	cut := len(trimmed) - keepTailMessages
	if cut < 0 {
		cut = 0
	}
	for index := 0; index < cut; index++ {
		trimmed[index] = elide(trimmed[index])
	}

	// Still over budget: drop the oldest messages, but stop at the floor and
	// never leave a tool result at the head.
	for EstimateMessages(trimmed) > budgetTokens && len(trimmed) > keepMinMessages {
		trimmed = trimmed[1:]
		for len(trimmed) > keepMinMessages && trimmed[0].Role == provider.RoleTool {
			trimmed = trimmed[1:]
		}
	}
	if EstimateMessages(trimmed) > budgetTokens {
		for index := 0; index < len(trimmed)-keepMinMessages; index++ {
			trimmed[index] = elide(trimmed[index])
		}
	}
	return trimmed
}

// elide replaces a message's payload with a marker, keeping its role so the
// sequence stays valid.
//
// A Message shares its ToolCalls and Images slices with the session that owns
// it, so the tool-call slice is cloned before its arguments are blanked.
// Writing through the shared backing array would rewrite the stored history:
// the arguments of an old call would be lost from the session file, not just
// from the request being trimmed.
func elide(message provider.Message) provider.Message {
	switch message.Role {
	case provider.RoleTool:
		message.Content = elidedToolBody
	case provider.RoleAssistant:
		message.Content = clipText(message.Content)
		message.Reasoning = ""
		if len(message.ToolCalls) > 0 {
			cloned := make([]provider.ToolCall, len(message.ToolCalls))
			copy(cloned, message.ToolCalls)
			for index := range cloned {
				cloned[index].Arguments = "{}"
			}
			message.ToolCalls = cloned
		}
	default:
		message.Content = clipText(message.Content)
	}
	message.Images = nil
	return message
}

func clipText(text string) string {
	if len(text) <= elidedTextLimit {
		return text
	}
	return text[:elidedTextLimit] + "... [trimmed]"
}

// defaultContextBudget is the history budget when a model's window is unknown.
const defaultContextBudget = 96000

// toolsAndOutputReserveTokens is held back from the window for the system
// prompt, the tool schemas and the model's own answer. Without it a long
// conversation fills the window and the request is rejected.
const toolsAndOutputReserveTokens = 32000

// HistoryBudget converts a model's context window into the token budget for
// the conversation history.
//
// The floor matters for a small local model: a 32k window minus a 32k reserve
// would leave nothing, so the budget never drops below 30 percent of the
// window. That is tight, but it fails loudly at the provider rather than
// silently discarding every earlier turn.
func HistoryBudget(contextWindow int) int {
	if contextWindow <= 0 {
		return defaultContextBudget
	}
	budget := contextWindow - toolsAndOutputReserveTokens
	floor := contextWindow * 30 / 100
	if budget < floor {
		budget = floor
	}
	return budget
}

// compactForModel trims a conversation using the configured budget.
func compactForModel(messages []provider.Message, budget int) []provider.Message {
	if budget <= 0 {
		budget = defaultContextBudget
	}
	return Compact(messages, budget)
}

// HistoryHint returns a short human summary of context usage for the status
// bar.
func HistoryHint(messages []provider.Message, budget int) string {
	if budget <= 0 {
		budget = defaultContextBudget
	}
	used := EstimateMessages(messages)
	percent := used * 100 / budget
	if percent > 100 {
		percent = 100
	}
	return fmt.Sprintf("%s/%s tokens (%d%%)", shortNumber(used), shortNumber(budget), percent)
}

func shortNumber(value int) string {
	if value < 1000 {
		return strconv.Itoa(value)
	}
	return fmt.Sprintf("%.1fk", float64(value)/1000)
}
