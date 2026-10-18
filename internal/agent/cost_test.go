package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

func usageChunk(prompt, completion int) provider.StreamEvent {
	return provider.StreamEvent{
		Type:  provider.EventUsage,
		Usage: &provider.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion},
	}
}

func TestOverBudgetOnlyFiresWithALimit(t *testing.T) {
	cases := []struct {
		budget float64
		spend  float64
		want   bool
	}{
		{0, 100, false},  // no limit means no limit
		{-1, 100, false}, // a negative limit is treated as absent
		{5, 4.99, false}, // under
		{5, 5.00, true},  // exactly at
		{5, 5.01, true},  // over
		{0.01, 0, false}, // nothing spent yet
	}
	for _, testCase := range cases {
		if got := overBudget(testCase.budget, testCase.spend); got != testCase.want {
			t.Errorf("overBudget(%v, %v) = %v, want %v", testCase.budget, testCase.spend, got, testCase.want)
		}
	}
}

// TestRunStopsWhenTheBudgetIsExceeded is the money guard: a run that keeps
// calling the model must stop once estimated spend passes the cap, and it must
// say so rather than stopping silently.
func TestRunStopsWhenTheBudgetIsExceeded(t *testing.T) {
	// Each step spends real money, so the second step trips a small budget.
	step := []provider.StreamEvent{
		usageChunk(100_000, 50_000),
		callChunk("c", "list_directory", `{"path":"."}`),
	}
	client := &fakeClient{steps: [][]provider.StreamEvent{step, step, step, step}}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.MaxSteps = 10
	runner.Pricing = provider.Pricing{InputPerMillion: 3.00, OutputPerMillion: 15.00}
	// 100k in + 50k out costs 0.30 + 0.75 = 1.05 per step.
	runner.CostBudgetUSD = 1.50
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "keep going"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	var stopReason string
	var budgetMessage string
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd {
			stopReason = event.StopReason
		}
		if event.Kind == EventError && event.Err != nil && strings.Contains(event.Err.Error(), "budget") {
			budgetMessage = event.Err.Error()
		}
	}
	if stopReason != "cost-cap" {
		t.Errorf("stop reason = %q, want cost-cap", stopReason)
	}
	if budgetMessage == "" {
		t.Errorf("the run must explain which budget it hit: %v", recorder.events)
	}
	if !strings.Contains(budgetMessage, "1.50") {
		t.Errorf("the message should name the budget, got %q", budgetMessage)
	}
	// Spend is recorded on the session so a resumed run keeps counting.
	if session.Cost() < 1.50 {
		t.Errorf("session cost = %v, want at least the budget", session.Cost())
	}
}

// TestRunWithoutABudgetIsUnlimited guards against a budget of zero being
// treated as "stop immediately".
func TestRunWithoutABudgetIsUnlimited(t *testing.T) {
	step := []provider.StreamEvent{usageChunk(1_000_000, 1_000_000), callChunk("c", "list_directory", `{"path":"."}`)}
	client := &fakeClient{steps: [][]provider.StreamEvent{step, step}}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.MaxSteps = 2
	runner.Pricing = provider.Pricing{InputPerMillion: 15.00, OutputPerMillion: 75.00}
	runner.CostBudgetUSD = 0
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "spend freely"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if session.Cost() <= 0 {
		t.Errorf("spend should still be tracked without a budget, got %v", session.Cost())
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for _, event := range recorder.events {
		if event.Kind == EventTurnEnd && event.StopReason == "cost-cap" {
			t.Errorf("no budget was set, so cost-cap must not fire")
		}
	}
}

// TestUsageEventsCarryTheRunningTotal is what the status bar reads.
func TestUsageEventsCarryTheRunningTotal(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{usageChunk(1000, 500), textChunk("done")},
	}}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.Pricing = provider.Pricing{InputPerMillion: 3.00, OutputPerMillion: 15.00}
	session := NewSession(t.TempDir(), "test-model")

	if err := runner.Run(context.Background(), session, "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	seen := false
	for _, event := range recorder.events {
		if event.Kind == EventUsage {
			seen = true
			if !event.CostKnown {
				t.Errorf("a priced model should report a known cost")
			}
			want := 1000.0/1_000_000*3.00 + 500.0/1_000_000*15.00
			if diff := event.CostUSD - want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("cost = %v, want %v", event.CostUSD, want)
			}
		}
	}
	if !seen {
		t.Fatalf("no usage event was emitted")
	}
}

// TestFreeModelReportsKnownZeroCost keeps "free" distinct from "unknown".
func TestFreeModelReportsKnownZeroCost(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{
		{usageChunk(5000, 2000), textChunk("done")},
	}}
	runner, _, recorder := newTestRunner(t, client, &ApprovalPolicy{Mode: ApprovalAll}, nil)
	runner.Pricing = provider.Pricing{} // a local model
	session := NewSession(t.TempDir(), "qwen2.5-coder:latest")

	if err := runner.Run(context.Background(), session, "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for _, event := range recorder.events {
		if event.Kind == EventUsage && event.CostKnown {
			t.Errorf("a local model has no price, so CostKnown should be false")
		}
	}
}
