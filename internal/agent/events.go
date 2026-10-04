// Package agent runs the tool-calling loop: it streams from a provider,
// executes the tools the model asks for, and reports every step as an Event
// so a terminal front end can render thinking, reasoning and tool activity.
package agent

import (
	"time"

	"github.com/99apps-id/termixgo/internal/provider"
)

// EventKind discriminates the events the loop emits.
type EventKind string

const (
	// EventThinking is a live reasoning fragment.
	EventThinking EventKind = "thinking"
	// EventReasoned closes a reasoning block with its duration.
	EventReasoned EventKind = "reasoned"
	// EventText is an answer fragment.
	EventText EventKind = "text"
	// EventToolStart announces a tool call about to run.
	EventToolStart EventKind = "tool-start"
	// EventToolEnd reports a finished tool call.
	EventToolEnd EventKind = "tool-end"
	// EventPlan reports a changed todo list.
	EventPlan EventKind = "plan"
	// EventNotice is informational output from the loop itself.
	EventNotice EventKind = "notice"
	// EventError is a failure the operator must see.
	EventError EventKind = "error"
	// EventApproval asks the operator to allow or deny a tool call.
	EventApproval EventKind = "approval"
	// EventTurnStart marks the start of one assistant turn.
	EventTurnStart EventKind = "turn-start"
	// EventTurnEnd closes a turn with its stop reason.
	EventTurnEnd EventKind = "turn-end"
	// EventUsage reports token accounting.
	EventUsage EventKind = "usage"
	// EventProcessEnd reports a worker process that asked to be announced when
	// it finishes. ToolName carries the process handle; Text is the notice.
	EventProcessEnd EventKind = "process-end"
)

// Event is one unit of agent activity.
type Event struct {
	Kind EventKind

	// Text carries thinking, answer and notice text.
	Text string

	// ToolName is the tool id; ToolLabel is the human phrase.
	ToolName  string
	ToolLabel string
	ToolArgs  string
	// Preview is a unified diff of what a file-changing tool is about to
	// write, computed before the tool ran while the old bytes are still on
	// disk. The approval dialog and the transcript both render it.
	Preview string
	// ToolResult is a short display form; ToolOK reports success.
	ToolResult string
	ToolOK     bool
	ToolMillis int64

	Approval *ApprovalRequest
	Plan     []Todo
	Usage    provider.Usage

	// CostUSD is the estimated spend of the run so far, and CostKnown
	// says whether every spend component had a recorded price. Without the
	// flag a free local model and an unpriced one would both read as zero.
	CostUSD   float64
	CostKnown bool
	// CostUnpriced counts spend components that had no price. Zero means
	// CostKnown is the whole story. It keeps one unpriced subagent from
	// erasing the known dollars the rest of the session earned: the known
	// spend stays in CostUSD and the unpriceable part is named, not hidden.
	CostUnpriced int

	// StopReason is set on EventTurnEnd: stop, step-cap, aborted, error.
	StopReason string
	Err        error
}

// emit is the callback a front end supplies. It must not block for long: the
// agent goroutine calls it inline.
type Emitter func(Event)

// durationMillis is a small helper for event payloads.
func durationMillis(d time.Duration) int64 { return d.Milliseconds() }
