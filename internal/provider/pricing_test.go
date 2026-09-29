package provider

import (
	"math"
	"testing"
)

// pickModel returns the first catalogue entry matching a predicate.
//
// Tests that need "a priced cloud model" rather than "claude-sonnet-4-5" stay
// correct when the catalogue is updated, which happens whenever a vendor ships
// a generation. Pinning an id instead turned a routine model update into a wall
// of unrelated failures.
func pickModel(t *testing.T, why string, match func(Model) bool) Model {
	t.Helper()
	for _, model := range Models() {
		if match(model) {
			return model
		}
	}
	t.Fatalf("no catalogue model matches: %s", why)
	return Model{}
}

func TestPricingIsKnownForCataloguedModels(t *testing.T) {
	// Every cloud model in the catalogue must carry a price. One that does not
	// means a cost budget would silently never fire for it.
	missing := []string{}
	for _, model := range Models() {
		if model.Free() || model.PriceVaries() {
			continue
		}
		if !model.Pricing().Known() {
			missing = append(missing, model.ID)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d catalogue models have no recorded price: %v", len(missing), missing)
	}
}

func TestRoutingModelsHaveNoListPrice(t *testing.T) {
	// A router bills at the price of whatever model it picks, so the only
	// correct answer is to admit the price is unknown rather than report zero.
	routers := 0
	for _, model := range Models() {
		if !model.PriceVaries() {
			continue
		}
		routers++
		if model.Pricing().Known() {
			t.Errorf("%s is a router, so the table must not price it", model.ID)
		}
		if _, known := model.CostModel(nil); known {
			t.Errorf("%s must report an unknown price, not free", model.ID)
		}
	}
	if routers == 0 {
		t.Fatalf("the catalogue lists no routing models, so this test proves nothing")
	}
}

func TestLocalModelsAreFreeAndKnown(t *testing.T) {
	// A local model costs nothing, but that is a fact about the model rather
	// than a gap in the table, so a budget line must be able to say "free"
	// rather than "unknown".
	locals := 0
	for _, model := range Models() {
		if !IsLocal(model.Provider) {
			continue
		}
		locals++
		if !model.Free() {
			t.Errorf("%s is served by the local provider %q but does not report as free", model.ID, model.Provider)
		}
		if cost := model.Cost(Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}); cost != 0 {
			t.Errorf("%s cost = %v, want 0 for a local model", model.ID, cost)
		}
		if _, known := model.CostModel(nil); !known {
			t.Errorf("%s is known-free, so a budget must be able to use it", model.ID)
		}
	}
	if locals == 0 {
		t.Fatalf("the catalogue lists no local models, so this test proves nothing")
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
	// And it must report the price as unknown rather than as free, because that
	// is the difference between a budget working and a budget being decoration.
	if _, known := model.CostModel(nil); known {
		t.Errorf("an unlisted cloud model must not claim a known price")
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
	model := pickModel(t, "a priced cloud model", func(m Model) bool {
		return !m.Free() && m.Pricing().Known()
	})
	price := model.Pricing()

	usage := Usage{PromptTokens: 200_000, CompletionTokens: 100_000}
	want := 200_000.0/1_000_000*price.InputPerMillion + 100_000.0/1_000_000*price.OutputPerMillion
	if got := model.Cost(usage); math.Abs(got-want) > 1e-9 {
		t.Errorf("%s cost = %v, want %v", model.ID, got, want)
	}
}

// TestCatalogueIntegrity checks the properties the picker, the price lookup and
// the model resolver all depend on. A typo here would otherwise only surface
// when someone selected that model.
func TestCatalogueIntegrity(t *testing.T) {
	seen := map[string]string{}
	for _, model := range Models() {
		if _, ok := ByID(model.Provider); !ok {
			t.Errorf("model %s names unknown provider %q", model.ID, model.Provider)
		}
		if model.Label == "" || model.Description == "" {
			t.Errorf("model %s needs a label and a description", model.ID)
		}
		// The id is the primary key: two entries sharing one would make
		// ModelByID ambiguous and the picker would silently switch provider.
		if previous, ok := seen[model.ID]; ok {
			t.Errorf("id %q is used by both %s and %s", model.ID, previous, model.Provider)
		}
		seen[model.ID] = model.Provider
	}
}
