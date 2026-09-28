package provider

import (
	"math"
	"testing"
)

func TestPricingIsKnownForCataloguedModels(t *testing.T) {
	// A priced cloud model must report a price, otherwise a budget would
	// silently never fire.
	for _, id := range []string{"gpt-5.4-mini", "claude-sonnet-4-5", "gemini-2.5-flash", "deepseek-chat", "grok-4"} {
		model, ok := ModelByID(id)
		if !ok {
			t.Fatalf("model %s is missing from the catalogue", id)
		}
		if !model.Pricing().Known() {
			t.Errorf("%s has no recorded price", id)
		}
	}
}

func TestLocalModelsAreFreeAndKnown(t *testing.T) {
	// Local models cost nothing, but that is a fact about the model, not a
	// gap in the table. A budget line must be able to say "free", not
	// "unknown".
	for _, id := range []string{"qwen2.5-coder:latest", "llama3.2:latest"} {
		model, ok := ModelByID(id)
		if !ok {
			t.Fatalf("model %s is missing", id)
		}
		if !model.Free() {
			t.Errorf("%s should be recognised as local, provider %q", id, model.Provider)
		}
		if cost := model.Cost(Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}); cost != 0 {
			t.Errorf("%s cost = %v, want 0 for a local model", id, cost)
		}
	}
}

func TestUnknownModelReportsNoPrice(t *testing.T) {
	model := Model{ID: "brand-new-model", Provider: "some-cloud"}
	if model.Pricing().Known() {
		t.Errorf("an unlisted model should not claim a price")
	}
	if cost := model.Cost(Usage{PromptTokens: 1_000_000}); cost != 0 {
		t.Errorf("an unlisted model should cost zero to the estimator, got %v", cost)
	}
}

func TestCostScalesWithTokens(t *testing.T) {
	price := Pricing{InputPerMillion: 3.00, OutputPerMillion: 15.00}

	// A million of each is exactly the list price.
	if got := price.Cost(Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}); math.Abs(got-18.00) > 1e-9 {
		t.Errorf("cost = %v, want 18.00", got)
	}
	// Half a million in, nothing out.
	if got := price.Cost(Usage{PromptTokens: 500_000}); math.Abs(got-1.50) > 1e-9 {
		t.Errorf("cost = %v, want 1.50", got)
	}
	// No tokens is no cost, not a negative.
	if got := price.Cost(Usage{}); got != 0 {
		t.Errorf("cost = %v, want 0", got)
	}
}

func TestModelCostMatchesItsPrice(t *testing.T) {
	model, ok := ModelByID("claude-sonnet-4-5")
	if !ok {
		t.Fatal("claude-sonnet-4-5 is missing")
	}
	usage := Usage{PromptTokens: 200_000, CompletionTokens: 100_000}
	want := 200_000.0/1_000_000*3.00 + 100_000.0/1_000_000*15.00
	if got := model.Cost(usage); math.Abs(got-want) > 1e-9 {
		t.Errorf("model cost = %v, want %v", got, want)
	}
}
