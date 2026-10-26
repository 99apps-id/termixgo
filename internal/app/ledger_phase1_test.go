package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/provider"
)

// TestQuotaTrackerSurvivesRestart persists the rolling window, builds a
// fresh tracker over the same state file, and checks the window is intact:
// a restart used to reset the local 5-hour gauge to zero.
func TestQuotaTrackerSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-state.json")

	first := NewQuotaTracker()
	first.SetStatePath(path)
	first.RecordUsage("anthropic", "claude-sonnet-5", provider.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})
	first.RecordUsage("anthropic", "claude-sonnet-5", provider.Usage{PromptTokens: 50, CompletionTokens: 10, TotalTokens: 60})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the state file should exist after recording: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("quota state mode = %v, want 0600", info.Mode().Perm())
	}

	second := NewQuotaTracker()
	second.SetStatePath(path)
	if err := second.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	stats := second.Stats("anthropic", RollingWindow)
	if stats.Requests != 2 || stats.TotalTokens != 180 {
		t.Errorf("restored window = %d requests / %d tokens, want 2 / 180", stats.Requests, stats.TotalTokens)
	}
}

// TestQuotaTrackerLoadDropsExpiredRecords writes a state file holding one
// record inside the window and one far outside it: the old one must be
// dropped on load rather than revived into a window it no longer belongs to.
func TestQuotaTrackerLoadDropsExpiredRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-state.json")
	state := quotaState{Records: []UsageRecord{
		{Timestamp: time.Now().Add(-30 * time.Minute), Provider: "anthropic", Model: "m", TotalTokens: 10},
		{Timestamp: time.Now().Add(-48 * time.Hour), Provider: "anthropic", Model: "m", TotalTokens: 999},
	}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}

	tracker := NewQuotaTracker()
	tracker.SetStatePath(path)
	if err := tracker.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	stats := tracker.Stats("anthropic", RollingWindow)
	if stats.Requests != 1 || stats.TotalTokens != 10 {
		t.Errorf("window after load = %d / %d, want only the fresh record (1 / 10)", stats.Requests, stats.TotalTokens)
	}
}

// TestTurnEndAttributesSubagentUsageToItsOwnModel drives the emit path the
// way a turn with one delegated run does, then checks the quota split: the
// subagent's tokens land on the subagent's provider, and the parent is
// charged only its own remainder. Before the fix the whole turn total was
// recorded under the parent's model.
func TestTurnEndAttributesSubagentUsageToItsOwnModel(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()
	defer application.Shutdown()

	subUsage := provider.Usage{PromptTokens: 300, CompletionTokens: 150, TotalTokens: 450}
	mainUsage := provider.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}

	application.Emit(agent.Event{Kind: agent.EventTurnStart})
	application.Emit(agent.Event{Kind: agent.EventUsage, Usage: mainUsage})
	application.Emit(agent.Event{Kind: agent.EventUsage, Usage: subUsage,
		UsageProvider: "anthropic", UsageModel: "claude-sonnet-5"})
	application.Emit(agent.Event{Kind: agent.EventTurnEnd, StopReason: "stop",
		Usage: mainUsage.Add(subUsage), CostUSD: 0, CostKnown: true})

	tracker := application.QuotaTracker()
	if stats := tracker.Stats("anthropic", RollingWindow); stats.TotalTokens != 450 || stats.Requests != 1 {
		t.Errorf("anthropic window = %d tokens / %d requests, want the subagent's 450 / 1", stats.TotalTokens, stats.Requests)
	}
	if stats := tracker.Stats("ollama", RollingWindow); stats.TotalTokens != 120 || stats.Requests != 1 {
		t.Errorf("ollama window = %d tokens / %d requests, want only the parent's own 120 / 1", stats.TotalTokens, stats.Requests)
	}

	// The same split must exist in the session segments and the ledger file.
	session := application.Session()
	segments := session.Segments()
	if len(segments) != 2 {
		t.Fatalf("session segments = %+v, want one per model", segments)
	}
	ledgerPath, err := agent.SessionLedgerPath(session.ID())
	if err != nil {
		t.Fatalf("SessionLedgerPath: %v", err)
	}
	records, err := agent.ReadUsageLedger(ledgerPath)
	if err != nil {
		t.Fatalf("ReadUsageLedger: %v", err)
	}
	kinds := map[string]int{}
	for _, record := range records {
		kinds[record.Kind]++
		if record.Kind == agent.LedgerKindSubagent && (record.Provider != "anthropic" || record.Model != "claude-sonnet-5") {
			t.Errorf("subagent record attributed to %s/%s, want anthropic/claude-sonnet-5", record.Provider, record.Model)
		}
	}
	if kinds[agent.LedgerKindMain] != 1 || kinds[agent.LedgerKindSubagent] != 1 {
		t.Errorf("ledger kinds = %v, want one main and one subagent record", kinds)
	}
	if got := session.Usage().TotalTokens; got != 570 {
		t.Errorf("session aggregate = %d, want the records' sum 570", got)
	}
}

// TestForceCompactRecordsCompactionUsage runs a real forced compaction over
// the test provider and checks the call landed in the ledger as a compaction
// record under the task model, where before it landed nowhere at all.
func TestForceCompactRecordsCompactionUsage(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()
	defer application.Shutdown()

	session := application.Session()
	session.AddUser("first request")
	session.AddAssistant("first answer", "", nil)
	session.AddUser("second request")
	session.AddAssistant("second answer", "", nil)

	compacted, err := application.ForceCompact(context.Background())
	if err != nil {
		t.Fatalf("ForceCompact: %v", err)
	}
	if !compacted {
		t.Fatalf("ForceCompact should have condensed the earlier turn")
	}

	ledgerPath, err := agent.SessionLedgerPath(session.ID())
	if err != nil {
		t.Fatalf("SessionLedgerPath: %v", err)
	}
	records, err := agent.ReadUsageLedger(ledgerPath)
	if err != nil {
		t.Fatalf("ReadUsageLedger: %v", err)
	}
	found := false
	for _, record := range records {
		if record.Kind == agent.LedgerKindCompaction {
			found = true
			if record.Provider != "ollama" || record.BillingMode != string(provider.BillingLocal) {
				t.Errorf("compaction record = %s / %s, want the task model's ollama / local", record.Provider, record.BillingMode)
			}
		}
	}
	if !found {
		t.Errorf("no compaction record in the ledger: %+v", records)
	}
	if len(session.Segments()) == 0 {
		t.Errorf("the compaction spend should appear in the session segments")
	}
}

// TestAuditTurnEntryCarriesLedgerFields runs one turn and checks its audit
// line carries the ledger fields (session, provider, model, billing mode)
// alongside the legacy in/out fields, so the two ledgers can be reconciled.
func TestAuditTurnEntryCarriesLedgerFields(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()
	defer application.Shutdown()

	if err := application.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	entries, err := application.AuditTail(20)
	if err != nil {
		t.Fatalf("AuditTail: %v", err)
	}
	for _, entry := range entries {
		if entry.Kind != "turn" {
			continue
		}
		if entry.SessionID == "" {
			t.Errorf("the turn entry should name its session: %+v", entry)
		}
		if entry.Provider != "ollama" || entry.Model == "" {
			t.Errorf("the turn entry should name its provider and model: %+v", entry)
		}
		if entry.BillingMode != string(provider.BillingLocal) {
			t.Errorf("the turn entry billing mode = %q, want local for the test provider", entry.BillingMode)
		}
		return
	}
	t.Errorf("no turn entry found in %v", entries)
}
