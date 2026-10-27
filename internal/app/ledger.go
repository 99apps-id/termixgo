package app

import (
	"strconv"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/provider"
)

// This file is where measured usage becomes ledger records. The emit path
// reports usage one event at a time; each event is folded into the session
// aggregate exactly as it always was, and additionally written as one record
// in the session's usage ledger, segmented by the provider, model and
// billing mode that spent it. The aggregate is the sum of the records, so
// the two can be checked against each other instead of drifting apart.

// pendingSubagentUsage is one delegated run's folded usage, held between its
// usage event and the turn's end. The turn-end total includes it, so the
// quota split at turn end needs to know how much of that total was the
// subagent's, and under which model.
type pendingSubagentUsage struct {
	provider string
	model    string
	usage    provider.Usage
}

// recordUsageEvent folds one usage event into the app and session totals and
// records it in the usage ledger. A delegated run's fold is recorded as a
// subagent record under the subagent's own model; a plain step is a main
// record under the active model.
func (a *App) recordUsageEvent(event agent.Event) {
	a.AddUsage(event.Usage)

	if event.UsageModel != "" || event.UsageProvider != "" {
		a.mu.Lock()
		a.turnPending = append(a.turnPending, pendingSubagentUsage{
			provider: event.UsageProvider, model: event.UsageModel, usage: event.Usage,
		})
		a.mu.Unlock()
	}

	session := a.currentSession()
	if session == nil {
		return
	}
	a.mu.Lock()
	a.turnStep++
	step := a.turnStep
	a.mu.Unlock()

	if event.UsageModel != "" || event.UsageProvider != "" {
		a.storeRecord(session, a.subagentUsageRecord(session, event, step), false)
		return
	}
	a.storeRecord(session, a.mainUsageRecord(session, event.Usage, step), false)
}

// mainUsageRecord builds the record for one step of the active model. The
// price is the one resolved when the model was chosen, overrides included:
// a record carries dollars only when that price is known.
func (a *App) mainUsageRecord(session *agent.Session, usage provider.Usage, step int) agent.UsageRecord {
	a.mu.Lock()
	model := a.model
	pricing := a.pricing
	known := a.costKnown
	a.mu.Unlock()

	mode := model.BillingMode()
	record := agent.UsageRecord{
		Time:        time.Now(),
		SessionID:   session.ID(),
		TurnID:      strconv.Itoa(session.Turns()),
		Step:        step,
		Kind:        agent.LedgerKindMain,
		Provider:    model.Provider,
		Model:       model.ID,
		BillingMode: string(mode),
		AuthType:    string(mode.AuthType()),
		Usage:       agent.BreakdownOf(usage),
		Source:      agent.UsageSourceProvider,
	}
	if known {
		cost := pricing.Cost(usage)
		record.CostUSD = &cost
	}
	return record
}

// subagentUsageRecord builds the record for a delegated run's folded usage,
// attributed to the subagent's own provider and model. Its dollars are
// priced from that model, never from the parent's.
func (a *App) subagentUsageRecord(session *agent.Session, event agent.Event, step int) agent.UsageRecord {
	mode := provider.BillingModeFor(event.UsageModel, event.UsageProvider)
	record := agent.UsageRecord{
		Time:        time.Now(),
		SessionID:   session.ID(),
		TurnID:      strconv.Itoa(session.Turns()),
		Step:        step,
		Kind:        agent.LedgerKindSubagent,
		Provider:    event.UsageProvider,
		Model:       event.UsageModel,
		BillingMode: string(mode),
		AuthType:    string(mode.AuthType()),
		Usage:       agent.BreakdownOf(event.Usage),
		Source:      agent.UsageSourceProvider,
	}
	if model, ok := provider.ModelByID(event.UsageModel); ok {
		if pricing, known := provider.PricedFor(a.Config(), model); known {
			cost := pricing.Cost(event.Usage)
			record.CostUSD = &cost
		}
	}
	return record
}

// recordTaskUsage records an internal task call (compaction or title) under
// the task model's own provider, model and billing mode, priced with that
// model's pricing. This closes the hole where those calls spent tokens that
// landed in no ledger at all. Their quota draw is recorded too: a plan's
// window does not care which part of the program spent against it.
func (a *App) recordTaskUsage(session *agent.Session, kind, wireModel string, usage provider.Usage) {
	if session == nil {
		return
	}
	modelID := wireModel
	providerID := ""
	var resolved provider.Model
	if model, ok := provider.ModelFromQuery(wireModel); ok {
		resolved = model
		providerID = model.Provider
		modelID = model.ID
	}
	mode := provider.BillingModeFor(modelID, providerID)
	record := agent.UsageRecord{
		Time:        time.Now(),
		SessionID:   session.ID(),
		TurnID:      strconv.Itoa(session.Turns()),
		Kind:        kind,
		Provider:    providerID,
		Model:       modelID,
		BillingMode: string(mode),
		AuthType:    string(mode.AuthType()),
		Usage:       agent.BreakdownOf(usage),
		Source:      agent.UsageSourceProvider,
	}
	if providerID != "" {
		if pricing, known := provider.PricedFor(a.Config(), resolved); known {
			cost := pricing.Cost(usage)
			record.CostUSD = &cost
		}
	}
	// A task call's aggregate was never folded by the emit path, so this
	// record folds everything: aggregate, cost and segment together.
	a.storeRecord(session, record, true)
	if tracker := a.QuotaTracker(); tracker != nil && usage.TotalTokens > 0 {
		tracker.RecordUsage(providerID, modelID, usage)
	}
}

// storeRecord folds a record into the session's segments, and its aggregate
// too when the caller has not already folded it, then appends it to the
// session's usage ledger file. Recording is best effort throughout: a turn
// never fails over its accounting.
//
// The record is stamped with the official quota window it drew against when
// the tracker holds a snapshot for its provider; without one the stamp
// stays null, because naming a window nobody identified would be inventing
// accounting.
func (a *App) storeRecord(session *agent.Session, record agent.UsageRecord, foldAggregate bool) {
	if record.QuotaRef == nil {
		if tracker := a.QuotaTracker(); tracker != nil {
			if ref := tracker.QuotaRef(record.Provider, record.Model); ref != "" {
				record.QuotaRef = &ref
			}
		}
	}
	if foldAggregate {
		session.RecordUsage(record)
	} else {
		session.FoldSegmentOnly(record)
	}
	if a.Ephemeral() {
		// An ephemeral run writes no session files; its ledger would be one.
		return
	}
	path, err := session.LedgerPath()
	if err != nil {
		return
	}
	ledger, err := agent.OpenUsageLedger(path)
	if err != nil {
		return
	}
	a.ledgerMu.Lock()
	defer a.ledgerMu.Unlock()
	_ = ledger.Append(record)
}

// subtractDelegated removes the delegated runs' usage from a turn total,
// leaving the parent's own spend. Each field is clamped at zero: a total
// that disagrees with its parts must not produce a negative record.
func subtractDelegated(total provider.Usage, pending []pendingSubagentUsage) provider.Usage {
	for _, delegated := range pending {
		total.PromptTokens -= delegated.usage.PromptTokens
		total.CompletionTokens -= delegated.usage.CompletionTokens
		total.TotalTokens -= delegated.usage.TotalTokens
		total.CacheReadTokens -= delegated.usage.CacheReadTokens
		total.CacheWriteTokens -= delegated.usage.CacheWriteTokens
		total.ReasoningTokens -= delegated.usage.ReasoningTokens
	}
	if total.PromptTokens < 0 {
		total.PromptTokens = 0
	}
	if total.CompletionTokens < 0 {
		total.CompletionTokens = 0
	}
	if total.TotalTokens < 0 {
		total.TotalTokens = 0
	}
	if total.CacheReadTokens < 0 {
		total.CacheReadTokens = 0
	}
	if total.CacheWriteTokens < 0 {
		total.CacheWriteTokens = 0
	}
	if total.ReasoningTokens < 0 {
		total.ReasoningTokens = 0
	}
	return total
}

// SessionSegments returns the live session's per-model spend segments, which
// is what /cost renders under the session total.
func (a *App) SessionSegments() []agent.UsageSegment {
	session := a.currentSession()
	if session == nil {
		return nil
	}
	return session.Segments()
}

// AuthUsage returns the active session's usage partitioned by auth category (BYOK vs OAuth).
func (a *App) AuthUsage() agent.AuthUsageSummary {
	session := a.currentSession()
	if session == nil {
		return agent.AuthUsageSummary{}
	}
	return session.AuthUsage()
}
