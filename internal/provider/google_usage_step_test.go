package provider

import "testing"

// TestGoogleUsageStepCountsTheIncrease unit-tests the tracker itself, including
// the counter that moves backwards.
func TestGoogleUsageStepCountsTheIncrease(t *testing.T) {
	tracker := &googleUsage{}

	step, ok := tracker.step(Usage{PromptTokens: 4, CompletionTokens: 1, TotalTokens: 5})
	if !ok || step != (Usage{PromptTokens: 4, CompletionTokens: 1, TotalTokens: 5}) {
		t.Fatalf("first step = %+v ok=%v", step, ok)
	}

	step, ok = tracker.step(Usage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7})
	if !ok || step != (Usage{CompletionTokens: 2, TotalTokens: 2}) {
		t.Errorf("second step = %+v ok=%v, want only the increase", step, ok)
	}

	if _, ok := tracker.step(Usage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7}); ok {
		t.Errorf("an unchanged report must not be counted again")
	}

	// A counter that goes backwards is not a charge, and the high-water mark
	// stops the ground already charged from being counted when it climbs back.
	if _, ok := tracker.step(Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}); ok {
		t.Errorf("a smaller report must not emit a negative step")
	}
	step, ok = tracker.step(Usage{PromptTokens: 4, CompletionTokens: 4, TotalTokens: 8})
	if !ok || step != (Usage{CompletionTokens: 1, TotalTokens: 1}) {
		t.Errorf("step after a dip = %+v ok=%v, want only the fresh increase", step, ok)
	}
}
