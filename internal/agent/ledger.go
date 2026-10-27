package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// This file is the usage ledger, version 2: one record per provider call,
// segmented by the provider, model and billing mode that actually spent the
// tokens.
//
// Version 1 was a single aggregate on the session. That made three different
// lies possible: internal calls (compaction, title) spent tokens that landed
// nowhere, a mid-session model switch folded two models' spend into one
// figure under whichever model was active at the end, and a subagent's tokens
// were charged to its parent's model. Records are the fix: the session
// aggregate stays as a display cache, but it is now the sum of records, and
// the records are kept per segment so /cost can show who spent what.

// Kinds of usage record. The compaction and title kinds are the task kinds in
// tasks.go, so a record names the job the same way the rest of the code does.
const (
	// LedgerKindMain is one step of the conversation's own run loop.
	LedgerKindMain = "main"
	// LedgerKindSubagent is one delegated run, folded into its parent turn.
	LedgerKindSubagent = "subagent"
	// LedgerKindCompaction is the internal condensation call (TaskCompaction).
	LedgerKindCompaction = TaskCompaction
	// LedgerKindTitle is the internal session-naming call (TaskTitle).
	LedgerKindTitle = TaskTitle
)

// Usage source values. A record carries the provider's own numbers; an
// estimated record is one rebuilt after the fact (a repaired session), and it
// is labelled rather than passed off as measured.
const (
	UsageSourceProvider  = "provider"
	UsageSourceEstimated = "estimated"
)

// UsageBreakdown is the per-record token split. It mirrors provider.Usage with
// the ledger's own field names, so a JSONL line reads the same way the audit
// spec names it.
type UsageBreakdown struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
	Total      int `json:"total"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	Reasoning  int `json:"reasoning"`
}

// BreakdownOf converts a provider usage report into ledger shape.
func BreakdownOf(usage provider.Usage) UsageBreakdown {
	return UsageBreakdown{
		Prompt:     usage.PromptTokens,
		Completion: usage.CompletionTokens,
		Total:      usage.TotalTokens,
		CacheRead:  usage.CacheReadTokens,
		CacheWrite: usage.CacheWriteTokens,
		Reasoning:  usage.ReasoningTokens,
	}
}

// Usage converts a breakdown back into a provider usage report.
func (b UsageBreakdown) Usage() provider.Usage {
	return provider.Usage{
		PromptTokens:     b.Prompt,
		CompletionTokens: b.Completion,
		TotalTokens:      b.Total,
		CacheReadTokens:  b.CacheRead,
		CacheWriteTokens: b.CacheWrite,
		ReasoningTokens:  b.Reasoning,
	}
}

// UsageRecord is one provider call's accounting.
//
// CostUSD is a pointer because null is information: a plan record has no
// dollar figure, and inventing one would be money the operator never pays. It
// is set only for the api_key mode, for a local model (a real zero), or when
// the operator priced the model themselves through an override. Credits is
// likewise set only when a credit-plan provider reports credits spent, which
// none of the current clients do; the field exists so the schema does not
// have to change when one starts. QuotaRef names the official quota window a
// plan record drew against; it stays null until a provider's window identity
// is wired through, rather than naming a window nobody identified.
type UsageRecord struct {
	Time        time.Time      `json:"ts"`
	SessionID   string         `json:"session_id"`
	TurnID      string         `json:"turn_id,omitempty"`
	Step        int            `json:"step,omitempty"`
	Kind        string         `json:"kind"`
	Provider    string         `json:"provider,omitempty"`
	Model       string         `json:"model,omitempty"`
	BillingMode string         `json:"billing_mode"`
	Usage       UsageBreakdown `json:"usage"`
	CostUSD     *float64       `json:"cost_usd"`
	Credits     *float64       `json:"credits"`
	QuotaRef    *string        `json:"quota_ref"`
	Source      string         `json:"source"`
}

// UsageSegment is the aggregate of every record that shares one provider,
// model and billing mode. It is what /cost renders per line, and what a
// resumed session reloads.
type UsageSegment struct {
	Provider    string         `json:"provider,omitempty"`
	Model       string         `json:"model,omitempty"`
	BillingMode string         `json:"billing_mode"`
	Usage       provider.Usage `json:"usage"`
	CostUSD     float64        `json:"costUsd,omitempty"`
	Records     int            `json:"records"`
	// Unpriced counts records in this segment that carried no dollar figure.
	// Zero means CostUSD is the segment's whole measured spend; nonzero means
	// it is a floor, which is the same distinction the session-level unpriced
	// count makes.
	Unpriced int `json:"unpriced,omitempty"`
}

// SegmentKey is the identity a record folds into.
func SegmentKey(providerName, model, billingMode string) string {
	return providerName + "\x00" + model + "\x00" + billingMode
}

// FoldSegment folds one record into a segment list, returning the updated
// list. The list is kept in first-seen order so a display reads in the order
// the spend happened.
func FoldSegment(segments []UsageSegment, record UsageRecord) []UsageSegment {
	key := SegmentKey(record.Provider, record.Model, record.BillingMode)
	for index := range segments {
		if SegmentKey(segments[index].Provider, segments[index].Model, segments[index].BillingMode) != key {
			continue
		}
		segments[index].Usage = segments[index].Usage.Add(record.Usage.Usage())
		segments[index].Records++
		if record.CostUSD != nil {
			segments[index].CostUSD += *record.CostUSD
		} else {
			segments[index].Unpriced++
		}
		return segments
	}
	segment := UsageSegment{
		Provider:    record.Provider,
		Model:       record.Model,
		BillingMode: record.BillingMode,
		Usage:       record.Usage.Usage(),
		Records:     1,
	}
	if record.CostUSD != nil {
		segment.CostUSD = *record.CostUSD
	} else {
		segment.Unpriced = 1
	}
	return append(segments, segment)
}

// SumRecords aggregates records into the session total and its segments. It
// is the definition of the aggregate: the session cache must equal this sum.
func SumRecords(records []UsageRecord) (provider.Usage, []UsageSegment) {
	var total provider.Usage
	var segments []UsageSegment
	for _, record := range records {
		total = total.Add(record.Usage.Usage())
		segments = FoldSegment(segments, record)
	}
	return total, segments
}

// SessionLedgerPath is where one session's usage records live: beside the
// session file, under the same id guard, so a crafted id can no more reach
// outside the sessions directory through the ledger than through the session.
func SessionLedgerPath(sessionID string) (string, error) {
	return SessionLedgerPathIn("", sessionID)
}

// SessionLedgerPathIn is SessionLedgerPath for one sessions directory. An
// empty dir means the shared directory, which is where the default agent's
// conversations and ledgers live.
func SessionLedgerPathIn(dir, sessionID string) (string, error) {
	if err := checkSessionID(sessionID); err != nil {
		return "", err
	}
	resolved, err := sessionsDir(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, sessionID+".ledger.jsonl"), nil
}

// UsageLedger appends usage records to one session's JSONL file.
type UsageLedger struct {
	mu   sync.Mutex
	path string
}

// OpenUsageLedger prepares a ledger at path. The file is created on the
// first record, like the audit ledger.
func OpenUsageLedger(path string) (*UsageLedger, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("the usage ledger path is empty")
	}
	return &UsageLedger{path: path}, nil
}

// Path reports the ledger file.
func (l *UsageLedger) Path() string { return l.path }

// Append writes one record. A failure is returned so the caller can decide it
// does not matter; a turn never fails over its accounting.
func (l *UsageLedger) Append(record UsageRecord) error {
	if record.Time.IsZero() {
		record.Time = time.Now()
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return fmt.Errorf("create usage ledger directory: %w", err)
	}
	file, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open usage ledger: %w", err)
	}
	defer file.Close()
	// A freshly created file takes its mode from the open call on POSIX; on
	// Windows the restriction has to be applied, as the session writer does.
	if err := secrets.RestrictFile(l.path); err != nil {
		return fmt.Errorf("protect usage ledger: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write usage ledger: %w", err)
	}
	return nil
}

// ReadUsageLedger reads every record in a session ledger file. A missing
// file is an empty ledger, not an error: sessions written before records
// existed have none, and their aggregate is the stored one.
func ReadUsageLedger(path string) ([]UsageRecord, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read usage ledger: %w", err)
	}
	defer file.Close()
	var records []UsageRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record UsageRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}
