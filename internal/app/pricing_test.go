package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

func TestPricingForPrefersAConfiguredOverride(t *testing.T) {
	application := newTestApp(t)

	model, ok := provider.ModelByID("claude-sonnet-4-5")
	if !ok {
		t.Fatal("claude-sonnet-4-5 is missing from the catalogue")
	}
	builtIn, known := application.pricingFor(model)
	if !known || !builtIn.Known() {
		t.Fatalf("the fixture model should have a built-in price, got %+v known=%v", builtIn, known)
	}

	if err := application.UpdateConfig(func(cfg *config.Config) {
		cfg.ModelPricing = map[string]config.ModelPrice{
			"claude-sonnet-4-5": {InputPerMillion: 0.01, OutputPerMillion: 0.02},
		}
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	overridden, known := application.pricingFor(model)
	if !known {
		t.Errorf("an override is a known price")
	}
	if overridden.InputPerMillion != 0.01 || overridden.OutputPerMillion != 0.02 {
		t.Errorf("the configured price was not used: %+v", overridden)
	}
	if overridden == builtIn {
		t.Errorf("the override should differ from the built-in price")
	}
}

// TestPricingForPricesAnUnknownModel is the end-to-end case: a model with no
// table entry becomes budgetable once the operator supplies a price, which is
// what makes a cost cap usable behind a custom endpoint.
func TestPricingForPricesAnUnknownModel(t *testing.T) {
	application := newTestApp(t)

	unknown := provider.Model{ID: "gateway/llama-3", Provider: "openai-compatible", Label: "gateway/llama-3"}
	if _, known := application.pricingFor(unknown); known {
		t.Fatalf("the fixture should start unpriced")
	}

	if err := application.UpdateConfig(func(cfg *config.Config) {
		cfg.ModelPricing = map[string]config.ModelPrice{
			"gateway/llama-3": {InputPerMillion: 0.2, OutputPerMillion: 0.4},
		}
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	priced, known := application.pricingFor(unknown)
	if !known {
		t.Fatalf("the configured price should make the model priced")
	}
	if priced.InputPerMillion != 0.2 {
		t.Errorf("price = %+v", priced)
	}
}

// TestPricingForTreatsALocalModelAsKnownFree is the distinction that a cost
// display depends on: a local model costs nothing, which is not the same as
// having no price at all.
func TestPricingForTreatsALocalModelAsKnownFree(t *testing.T) {
	application := newTestApp(t)

	local, ok := provider.ModelByID("qwen2.5-coder:latest")
	if !ok {
		t.Fatal("the local fixture model is missing")
	}
	price, known := application.pricingFor(local)
	if !known {
		t.Errorf("a local model should report a known price of zero, not unknown")
	}
	if price.InputPerMillion != 0 || price.OutputPerMillion != 0 {
		t.Errorf("a local model should cost nothing, got %+v", price)
	}
}

func TestPricingForWithoutOverridesMatchesTheTable(t *testing.T) {
	application := newTestApp(t)
	model, _ := provider.ModelByID("gpt-5.4-mini")
	got, known := application.pricingFor(model)
	if !known || got != model.Pricing() {
		t.Errorf("with no overrides the table price should be used: %+v known=%v", got, known)
	}
}

func TestProcessManagerIsAvailableAndShared(t *testing.T) {
	application := newTestApp(t)
	if application.Processes() == nil {
		t.Fatalf("the app must expose a process manager")
	}
	// The same manager must reach a tool environment, or a process started by
	// one run could never be read by the next.
	if env := application.env(); env.Processes != application.Processes() {
		t.Errorf("the tool environment should share the app's process manager")
	}
}

// TestCostIsKnownBeforeTheFirstTurn is the display guard: the status bar and
// /cost are read the moment the app starts, so a known-free local model must
// not read as "unknown" until a turn has produced a usage event.
func TestCostIsKnownBeforeTheFirstTurn(t *testing.T) {
	application := newTestApp(t)
	if err := application.Secrets().Set(secrets.ProviderKey("openai"), "sk-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SetModelByQuery("qwen2.5-coder:latest"); err != nil {
		t.Fatalf("SetModelByQuery: %v", err)
	}

	spend, known := application.Cost()
	if !known {
		t.Errorf("a local model is known-free and must not report as unknown")
	}
	if spend != 0 {
		t.Errorf("spend before any turn = %v, want 0", spend)
	}
}

// TestCostIsKnownForAPricedModelBeforeAnyTurn covers the cloud case.
func TestCostIsKnownForAPricedModelBeforeAnyTurn(t *testing.T) {
	application := newTestApp(t)
	if err := application.Secrets().Set(secrets.ProviderKey("anthropic"), "sk-ant-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SetModelByQuery("claude-sonnet-4-5"); err != nil {
		t.Fatalf("SetModelByQuery: %v", err)
	}
	if _, known := application.Cost(); !known {
		t.Errorf("a model in the price table should report a known cost before any turn")
	}
}

// TestCostIsUnknownForAnUnpricedModel keeps the honest case: with no table
// entry and no override, the app must say so rather than claim zero.
func TestCostIsUnknownForAnUnpricedModel(t *testing.T) {
	application := newTestApp(t)
	if err := application.Secrets().Set(secrets.ProviderKey("openai"), "sk-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SetModelByQuery("openai:brand-new-unlisted-model"); err != nil {
		t.Fatalf("SetModelByQuery: %v", err)
	}
	if _, known := application.Cost(); known {
		t.Errorf("an unpriced model must report an unknown cost, not a zero one")
	}
}

func TestShutdownIsSafeWithNothingRunning(t *testing.T) {
	application := newTestApp(t)
	application.Shutdown()
	application.Shutdown()
}
