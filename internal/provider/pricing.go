package provider

import (
	"strings"

	"github.com/99apps-id/termixgo/internal/config"
)

// Pricing is the cost of a model in US dollars per million tokens.
//
// These are published list prices, kept for budgeting rather than billing.
// Vendors change them, and discounts, caching and batch tiers are not modelled,
// so treat a figure from here as an estimate with a margin of error, never as
// an invoice. Update the table when a number visibly drifts.
type Pricing struct {
	InputPerMillion  float64
	OutputPerMillion float64
}

// Known reports whether a price is recorded for this model. A zero price is
// legitimate for a local model, so callers need this to tell "free" from
// "unknown" before showing a number.
func (p Pricing) Known() bool { return p.InputPerMillion > 0 || p.OutputPerMillion > 0 }

// pricingTable maps a model id or wire id to its price.
var pricingTable = map[string]Pricing{
	"gpt-5.4-mini": {0.25, 2.00},
	"gpt-5.4":      {1.25, 10.00},
	"gpt-5.6":      {2.50, 20.00},
	"gpt-4.1":      {2.00, 8.00},
	"o4-mini":      {1.10, 4.40},

	"claude-sonnet-4-5":        {3.00, 15.00},
	"claude-opus-4-1":          {15.00, 75.00},
	"claude-haiku-4-5":         {1.00, 5.00},
	"claude-3-7-sonnet-latest": {3.00, 15.00},

	"gemini-3-pro":     {2.00, 12.00},
	"gemini-2.5-pro":   {1.25, 10.00},
	"gemini-2.5-flash": {0.30, 2.50},

	"deepseek-chat":     {0.27, 1.10},
	"deepseek-reasoner": {0.55, 2.19},

	"llama-3.3-70b-versatile": {0.59, 0.79},
	"openai/gpt-oss-120b":     {0.15, 0.75},
	"qwen/qwen3-32b":          {0.29, 0.59},

	"grok-4":      {3.00, 15.00},
	"grok-3-mini": {0.30, 0.50},

	"llama-3.3-70b": {0.85, 1.20},
	"qwen-3-32b":    {0.40, 0.80},

	"mistral-large-latest": {2.00, 6.00},
	"codestral-latest":     {0.30, 0.90},

	"anthropic/claude-sonnet-4.5": {3.00, 15.00},
}

// Local providers run on the operator's own hardware, so their marginal cost
// is zero. Listing them explicitly keeps "unknown" distinct from "free".
var localProviders = map[string]bool{
	"ollama": true, "lmstudio": true, "mlx": true,
}

// Pricing returns the recorded price for a model, or the zero value when none
// is known.
func (m Model) Pricing() Pricing {
	return m.PricingWith(nil)
}

// PricingWith returns the price to use for a model, preferring an operator
// override over the built-in table.
//
// The override is consulted by stable id first and wire id second, so a config
// entry keeps working when a vendor renames the model behind a stable id. This
// is what makes a cost budget meaningful for a model the table does not list.
func (m Model) PricingWith(overrides map[string]Pricing) Pricing {
	if price, ok := overrides[m.ID]; ok && price.Known() {
		return price
	}
	if wire := m.WireID(); wire != m.ID {
		if price, ok := overrides[wire]; ok && price.Known() {
			return price
		}
	}
	if price, ok := pricingTable[m.ID]; ok {
		return price
	}
	if wire := m.WireID(); wire != m.ID {
		if price, ok := pricingTable[wire]; ok {
			return price
		}
	}
	return Pricing{}
}

// Cost estimates the dollars a usage report costs at this price. An unknown
// price yields zero, and callers use Known() to say unknown, not zero.
func (p Pricing) Cost(usage Usage) float64 {
	return float64(usage.PromptTokens)/1_000_000*p.InputPerMillion +
		float64(usage.CompletionTokens)/1_000_000*p.OutputPerMillion
}

// Cost estimates the dollars a usage report costs on this model. An unknown
// price yields zero, and callers use Pricing().Known() to say "unknown"
// rather than "$0.00".
func (m Model) Cost(usage Usage) float64 {
	price := m.Pricing()
	return float64(usage.PromptTokens)/1_000_000*price.InputPerMillion +
		float64(usage.CompletionTokens)/1_000_000*price.OutputPerMillion
}

// CostModel resolves the price to use for a model and whether a price is
// actually known for it.
//
// The second return value is what separates three cases that all look like
// zero dollars: a local model that is genuinely free, a model with a recorded
// price, and a model nobody has priced. Only the last should tell the operator
// that a cost budget cannot work.
func (m Model) CostModel(overrides map[string]Pricing) (Pricing, bool) {
	price := m.PricingWith(overrides)
	if price.Known() {
		return price, true
	}
	if m.Free() {
		return Pricing{}, true
	}
	return Pricing{}, false
}

// Free reports whether the model runs locally, where tokens cost nothing
// beyond electricity.
func (m Model) Free() bool { return localProviders[m.Provider] }

// OverridesFrom converts the configured price list into the shape the resolver
// takes. A nil result means the operator has configured nothing, which keeps
// the common case free of an allocation.
func OverridesFrom(prices map[string]config.ModelPrice) map[string]Pricing {
	if len(prices) == 0 {
		return nil
	}
	overrides := make(map[string]Pricing, len(prices))
	for id, price := range prices {
		overrides[id] = Pricing{
			InputPerMillion:  price.InputPerMillion,
			OutputPerMillion: price.OutputPerMillion,
		}
	}
	return overrides
}

// PricedFor resolves what a model costs under the operator's own price list,
// and whether that figure is real.
//
// A caller needs the second value because it separates three states that all
// look like zero: a local model that is genuinely free, a model whose price is
// known, and one nobody has priced. Only the last means a cost cap cannot
// work, and that is worth telling the operator about.
func PricedFor(cfg config.Config, model Model) (Pricing, bool) {
	return model.CostModel(OverridesFrom(cfg.ModelPricing))
}

// PricedByID resolves the same question for a model id, including one the
// catalogue does not list. It is what lets a diagnostic answer "can this cap
// fire at all" before a single token has been spent.
func PricedByID(cfg config.Config, id string) (Pricing, bool) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return Pricing{}, false
	}
	if model, ok := ModelByID(trimmed); ok {
		return PricedFor(cfg, model)
	}

	model := Model{ID: trimmed}
	// A local server's tags keep their colon: it belongs to the tag, not to a
	// provider prefix.
	if index := strings.Index(trimmed, ":"); index > 0 && !IsLocal(trimmed) {
		model.Provider = trimmed[:index]
	}
	if price, known := PricedFor(cfg, model); known {
		return price, true
	}
	// A hand-edited config can hold the prefixed spelling of a model the app
	// would store bare, so the unprefixed form is tried as well. Without this
	// the same model would be priced or not depending on who wrote the config.
	if index := strings.Index(trimmed, ":"); index > 0 && model.Provider != "" {
		bare := Model{ID: trimmed[index+1:], Provider: model.Provider}
		return PricedFor(cfg, bare)
	}
	return Pricing{}, false
}
