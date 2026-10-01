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

	// Protect the whole current turn, not just the last few messages. A turn
	// with several tool calls is longer than the message-count window, so
	// counting messages alone elided the turn's own earlier tool results while
	// it was still running: the model watched its context disappear mid-task and
	// answered from what was left. Only the messages before the turn may be
	// trimmed here.
	protected := currentTurnStart(trimmed)
	if byCount := len(trimmed) - keepTailMessages; byCount < protected {
		protected = byCount
	}
	if protected < 0 {
		protected = 0
	}
	for index := 0; index < protected; index++ {
		trimmed[index] = elide(trimmed[index])
	}

	// Still over budget: drop the oldest messages, but never into the protected
	// turn. A request may not begin with a tool result, so a drop that would
	// put one at the head is refused and the head stays on the assistant turn
	// that owns those results. Being a little over budget is a smaller problem
	// than a sequence the provider rejects.
	for EstimateMessages(trimmed) > budgetTokens && protected > 0 && len(trimmed) > keepMinMessages {
		candidate := trimmed[1:]
		if len(candidate) == 0 || candidate[0].Role == provider.RoleTool {
			break
		}
		trimmed = candidate
		protected--
	}

	// The protected turn alone can still be over budget, which the provider
	// rejects. Elide from its oldest end as a last resort, keeping the newest
	// messages whole.
	if EstimateMessages(trimmed) > budgetTokens {
		for index := 0; index < len(trimmed)-keepMinMessages; index++ {
			if EstimateMessages(trimmed) <= budgetTokens {
				break
			}
			trimmed[index] = elide(trimmed[index])
		}
	}
	return trimmed
}

// currentTurnStart is the index of the last user message, which begins the turn
// being answered. Everything before it belongs to an earlier turn and may be
// trimmed; the messages from it onward are the live context the model needs to
// finish the current turn.
func currentTurnStart(messages []provider.Message) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == provider.RoleUser {
			return index
		}
	}
	return 0
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
	return clipBytes(text, elidedTextLimit) + "... [trimmed]"
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
// A large window is not a licence to fill it. The reserve is at least half the
// window, so history never takes more than half: a request that runs close to
// the limit is where a weaker model loses the thread, and where a provider may
// silently truncate the start. Budgeting to a few percent under the window, as
// this once did, left a 262k model with a 230k history and invited exactly that.
//
// The floor still matters for a small local model: a 32k window minus a 32k
// reserve would leave nothing, so the budget never drops below 30 percent of
// the window.
func HistoryBudget(contextWindow int) int {
	if contextWindow <= 0 {
		return defaultContextBudget
	}
	reserve := toolsAndOutputReserveTokens
	if half := contextWindow / 2; half > reserve {
		reserve = half
	}
	budget := contextWindow - reserve
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
