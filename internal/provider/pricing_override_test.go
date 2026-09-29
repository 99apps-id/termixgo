package provider

import (
	"math"
	"testing"
)

func TestPricingWithPrefersAnOverride(t *testing.T) {
	model := pickModel(t, "a priced cloud model", func(m Model) bool {
		return !m.Free() && m.Pricing().Known()
	})
	builtIn := model.Pricing()

	// An operator who knows better than the table wins, which is the whole
	// point: a reseller or a gateway has its own prices.
	overrides := map[string]Pricing{model.ID: {InputPerMillion: 1, OutputPerMillion: 2}}
	got := model.PricingWith(overrides)
	if got.InputPerMillion != 1 || got.OutputPerMillion != 2 {
		t.Errorf("override ignored: %+v", got)
	}
	if got == builtIn {
		t.Errorf("the override should differ from the built-in price")
	}
}

func TestPricingWithFallsBackToTheTable(t *testing.T) {
	model := pickModel(t, "a priced cloud model", func(m Model) bool {
		return !m.Free() && m.Pricing().Known()
	})
	if got := model.PricingWith(map[string]Pricing{"something-else": {1, 2}}); got != model.Pricing() {
		t.Errorf("an unrelated override must not change the price: %+v", got)
	}
	if got := model.PricingWith(nil); got != model.Pricing() {
		t.Errorf("no overrides should mean the table price: %+v", got)
	}
}

func TestPricingWithIgnoresAnEmptyOverride(t *testing.T) {
	model, _ := ModelByID("gpt-5.4-mini")
	// A zero entry is not a price of zero, so it must not shadow the real one.
	got := model.PricingWith(map[string]Pricing{"gpt-5.4-mini": {}})
	if got != model.Pricing() {
		t.Errorf("a zero override should be ignored, got %+v", got)
	}
}

func TestPricingWithMatchesTheWireID(t *testing.T) {
	// A model whose local id differs from the wire id must be found by either,
	// so an entry keeps working when a vendor renames the model.
	model := Model{ID: "stable-local-id", Provider: "openai", APIID: "vendor/new-name"}
	overrides := map[string]Pricing{"vendor/new-name": {InputPerMillion: 4, OutputPerMillion: 8}}
	got := model.PricingWith(overrides)
	if got.InputPerMillion != 4 {
		t.Errorf("the wire id should be matched, got %+v", got)
	}

	byLocal := map[string]Pricing{"stable-local-id": {InputPerMillion: 1, OutputPerMillion: 1}}
	if got := model.PricingWith(byLocal); got.InputPerMillion != 1 {
		t.Errorf("the local id should be matched, got %+v", got)
	}
}

func TestPricingWithMakesAnUnknownModelBudgetable(t *testing.T) {
	// This is the case the feature exists for: a model behind a custom
	// endpoint has no table entry, so without an override no budget can fire.
	model := Model{ID: "gateway/llama-3", Provider: "openai-compatible"}
	if model.Pricing().Known() {
		t.Fatalf("the fixture should start unpriced")
	}
	priced := model.PricingWith(map[string]Pricing{"gateway/llama-3": {InputPerMillion: 0.2, OutputPerMillion: 0.4}})
	if !priced.Known() {
		t.Fatalf("the override should make the model priced")
	}

	usage := Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}
	if cost := priced.Cost(usage); math.Abs(cost-0.6) > 1e-9 {
		t.Errorf("cost = %v, want 0.6", cost)
	}
}
