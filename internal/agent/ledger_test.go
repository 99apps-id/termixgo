package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
)

// TestTaskCallReturnsUsage pins the fix for the discarded internal spend: a
// task call's usage events are summed and returned, so the caller can record
// them instead of the tokens vanishing.
func TestTaskCallReturnsUsage(t *testing.T) {
	client := &taskClientStub{answer: "the brief"}
	brief, usage, err := SummarizeHistory(context.Background(), client, "cheap-model", "", "USER: hi")
	if err != nil {
		t.Fatalf("SummarizeHistory: %v", err)
	}
	if brief != "the brief" {
		t.Errorf("brief = %q", brief)
	}
	want := provider.Usage{PromptTokens: 100, CompletionTokens: 25, TotalTokens: 125, ReasoningTokens: 5}
	if usage != want {
		t.Errorf("usage = %+v, want the sum of the call's usage events %+v", usage, want)
	}

	title, titleUsage, err := SuggestTitle(context.Background(), client, "cheap-model", "", "USER: hi")
	if err != nil {
		t.Fatalf("SuggestTitle: %v", err)
	}
	if title != "the brief" {
		t.Errorf("title = %q", title)
	}
	if titleUsage != want {
		t.Errorf("title usage = %+v, want %+v", titleUsage, want)
	}
}

// TestSessionSegmentsStayPerModel records usage for two models and a task
// kind, then checks the segments never mix while the aggregate stays their
// sum. This is the segmentation the single-aggregate ledger could not do.
func TestSessionSegmentsStayPerModel(t *testing.T) {
	session := NewSession(t.TempDir(), "model-a")
	mainCost := 0.50
	session.RecordUsage(UsageRecord{
		Kind: LedgerKindMain, Provider: "openai", Model: "model-a",
		BillingMode: string(provider.BillingAPIKey),
		Usage:       BreakdownOf(provider.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110}),
		CostUSD:     &mainCost, Source: UsageSourceProvider,
	})
	session.RecordUsage(UsageRecord{
		Kind: LedgerKindSubagent, Provider: "anthropic", Model: "model-b",
		BillingMode: string(provider.BillingAPIKey),
		Usage:       BreakdownOf(provider.Usage{PromptTokens: 40, CompletionTokens: 5, TotalTokens: 45}),
		Source:      UsageSourceProvider,
	})
	session.RecordUsage(UsageRecord{
		Kind: LedgerKindCompaction, Provider: "anthropic", Model: "model-b",
		BillingMode: string(provider.BillingAPIKey),
		Usage:       BreakdownOf(provider.Usage{PromptTokens: 20, TotalTokens: 20}),
		Source:      UsageSourceProvider,
	})

	segments := session.Segments()
	if len(segments) != 2 {
		t.Fatalf("segments = %d, want one per (provider, model, mode): %+v", len(segments), segments)
	}
	if segments[0].Model != "model-a" || segments[0].Usage.TotalTokens != 110 || segments[0].Records != 1 {
		t.Errorf("segment A = %+v", segments[0])
	}
	if segments[0].CostUSD != mainCost || segments[0].Unpriced != 0 {
		t.Errorf("segment A cost = %v unpriced %d, want %v and 0", segments[0].CostUSD, segments[0].Unpriced, mainCost)
	}
	if segments[1].Model != "model-b" || segments[1].Usage.TotalTokens != 65 || segments[1].Records != 2 {
		t.Errorf("segment B = %+v, want both the subagent and compaction records", segments[1])
	}
	if segments[1].Unpriced != 2 {
		t.Errorf("segment B unpriced = %d, want 2 (neither record carried dollars)", segments[1].Unpriced)
	}
	if got := session.Usage().TotalTokens; got != 175 {
		t.Errorf("aggregate total = %d, want the segment sum 175", got)
	}
	if got := session.Cost(); got != mainCost {
		t.Errorf("aggregate cost = %v, want only the priced record's %v", got, mainCost)
	}

	total, summed := SumRecords([]UsageRecord{
		{Kind: LedgerKindMain, Provider: "openai", Model: "model-a", BillingMode: "api_key", Usage: BreakdownOf(provider.Usage{TotalTokens: 110}), CostUSD: &mainCost},
		{Kind: LedgerKindSubagent, Provider: "anthropic", Model: "model-b", BillingMode: "api_key", Usage: BreakdownOf(provider.Usage{TotalTokens: 45})},
		{Kind: LedgerKindCompaction, Provider: "anthropic", Model: "model-b", BillingMode: "api_key", Usage: BreakdownOf(provider.Usage{TotalTokens: 20})},
	})
	if total.TotalTokens != 175 || len(summed) != 2 {
		t.Errorf("SumRecords = %d tokens over %d segments, want 175 over 2", total.TotalTokens, len(summed))
	}
}

// TestSessionSegmentsSurviveSaveLoad persists segments in the session file
// and reloads them: a resumed session must keep its per-model split, not
// collapse back into one aggregate under the active model.
func TestSessionSegmentsSurviveSaveLoad(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	session := NewSession(t.TempDir(), "model-a")
	cost := 0.25
	session.RecordUsage(UsageRecord{
		Kind: LedgerKindMain, Provider: "openai", Model: "model-a", BillingMode: "api_key",
		Usage: BreakdownOf(provider.Usage{PromptTokens: 10, TotalTokens: 10}), CostUSD: &cost, Source: UsageSourceProvider,
	})
	session.RecordUsage(UsageRecord{
		Kind: LedgerKindTitle, Provider: "claude-oauth", Model: "model-b", BillingMode: "oauth_plan",
		Usage: BreakdownOf(provider.Usage{PromptTokens: 5, TotalTokens: 5}), Source: UsageSourceProvider,
	})
	if err := session.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := LoadSession(session.ID())
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	segments := loaded.Segments()
	if len(segments) != 2 {
		t.Fatalf("loaded segments = %+v, want 2", segments)
	}
	if segments[1].BillingMode != "oauth_plan" || segments[1].Unpriced != 1 || segments[1].CostUSD != 0 {
		t.Errorf("plan segment = %+v, want oauth_plan with no invented dollars", segments[1])
	}
	if loaded.Usage().TotalTokens != 15 || loaded.Cost() != cost {
		t.Errorf("loaded aggregate = %d tokens / $%v, want 15 / $%v", loaded.Usage().TotalTokens, loaded.Cost(), cost)
	}
}

// TestOldSessionJSONStillLoads writes a session file in the pre-ledger shape
// (no segments field at all) and loads it: old sessions must keep working,
// with their stored aggregate intact and no segments invented for them.
func TestOldSessionJSONStillLoads(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	dir, err := config.SessionsDir()
	if err != nil {
		t.Fatalf("SessionsDir: %v", err)
	}
	legacy := map[string]any{
		"id": "legacy01", "title": "old session", "workspace": "/tmp", "model": "model-a",
		"createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z",
		"messages": []any{},
		"usage":    map[string]any{"PromptTokens": 10, "CompletionTokens": 2, "TotalTokens": 12},
		"costUsd":  0.10,
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy01.json"), data, 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	loaded, err := LoadSession("legacy01")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded.Usage().TotalTokens != 12 || loaded.Cost() != 0.10 {
		t.Errorf("legacy aggregate = %d / $%v, want the stored 12 / $0.10", loaded.Usage().TotalTokens, loaded.Cost())
	}
	if got := loaded.Segments(); len(got) != 0 {
		t.Errorf("legacy session segments = %v, want none invented", got)
	}
}

// TestUsageLedgerRoundTrip writes records as JSONL and reads them back,
// checking the on-disk shape carries the fields reconciliation needs and
// that an oauth_plan record serialises its cost as an explicit null, not a
// zero dressed up as a measurement.
func TestUsageLedgerRoundTrip(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	path, err := SessionLedgerPath("abc123")
	if err != nil {
		t.Fatalf("SessionLedgerPath: %v", err)
	}
	ledger, err := OpenUsageLedger(path)
	if err != nil {
		t.Fatalf("OpenUsageLedger: %v", err)
	}
	record := UsageRecord{
		SessionID: "abc123", TurnID: "1", Step: 2, Kind: LedgerKindMain,
		Provider: "claude-oauth", Model: "claude-x", BillingMode: "oauth_plan",
		Usage:  BreakdownOf(provider.Usage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13, CacheReadTokens: 4, ReasoningTokens: 1}),
		Source: UsageSourceProvider,
	}
	if err := ledger.Append(record); err != nil {
		t.Fatalf("Append: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	for _, want := range []string{`"billing_mode":"oauth_plan"`, `"cost_usd":null`, `"credits":null`, `"quota_ref":null`, `"cache_read":4`, `"reasoning":1`, `"source":"provider"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("ledger line missing %s: %s", want, raw)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat ledger: %v", err)
	}
	// On Windows the 0600 mode is not how the file is protected: NTFS has no
	// Unix mode bits (Go reports 0666), and secrets.RestrictFile applies a
	// DACL instead. The mode assertion only means something on Unix.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("ledger mode = %v, want 0600", info.Mode().Perm())
	}
	records, err := ReadUsageLedger(path)
	if err != nil {
		t.Fatalf("ReadUsageLedger: %v", err)
	}
	if len(records) != 1 || records[0].Kind != LedgerKindMain || records[0].Usage.Total != 13 || records[0].CostUSD != nil {
		t.Errorf("records = %+v", records)
	}
	missing, err := ReadUsageLedger(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil || len(missing) != 0 {
		t.Errorf("a missing ledger should read empty, got %v, %v", missing, err)
	}
}
