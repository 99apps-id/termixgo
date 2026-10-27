package provider

import "strings"

// BillingMode names how a model's tokens are paid for. It is a property of a
// usage record, decided when the record is made from the provider and model,
// never guessed later by a display: the same token count means a dollar figure
// on an API key, a slice of a subscription window on an OAuth plan, a credit
// balance on a credit plan, and nothing at all on a local model. Mixing the
// modes up is how a gauge ends up pricing a subscription or quota-capping a
// pay-as-you-go key.
type BillingMode string

const (
	// BillingAPIKey is pay-per-token against a published list price.
	BillingAPIKey BillingMode = "api_key"
	// BillingOAuthPlan is a subscription reached through an OAuth login. Its
	// currency is the provider's quota window, never dollars per token.
	BillingOAuthPlan BillingMode = "oauth_plan"
	// BillingCreditPlan is a subscription that deducts credits (Qwen Token
	// Plan, StepFun Step Plan). Credits convert to dollars only through a
	// vendor-published rate, never through a per-token price.
	BillingCreditPlan BillingMode = "credit_plan"
	// BillingLocal runs on the operator's own hardware and costs nothing.
	BillingLocal BillingMode = "local"
	// BillingUnknown is a model no mode can be decided for: a router whose
	// price varies, or a catalogue gap. Unknown is stated, not papered over.
	BillingUnknown BillingMode = "unknown"
)

// Authentication and billing categories distinguishing BYOK vs OAuth.
const (
	AuthBYOK    = "byok"
	AuthOAuth   = "oauth"
	AuthCredit  = "credit"
	AuthLocal   = "local"
	AuthUnknown = "unknown"
)

// AuthType returns the human-readable authentication / billing category:
// "byok" for pay-per-token API keys, "oauth" for OAuth subscriptions,
// "credit" for credit plan subscriptions, "local" for local models.
func (b BillingMode) AuthType() string {
	switch b {
	case BillingAPIKey:
		return AuthBYOK
	case BillingOAuthPlan:
		return AuthOAuth
	case BillingCreditPlan:
		return AuthCredit
	case BillingLocal:
		return AuthLocal
	default:
		return AuthUnknown
	}
}

// IsBYOK reports whether usage was billed via Bring-Your-Own-Key (API Key).
func (b BillingMode) IsBYOK() bool { return b == BillingAPIKey }

// IsOAuth reports whether usage was billed via an OAuth subscription plan.
func (b BillingMode) IsOAuth() bool { return b == BillingOAuthPlan }

// BillingMode decides how this model's usage is billed.
//
// The order matters. A local model is free whatever its wire name collides
// with. A plan provider's models are plan-billed even when a same-named model
// has a dollar price elsewhere, because the operator on that provider never
// pays per token. An OAuth provider outside the plan table is still a
// subscription. Only then does a known list price mean pay-per-token.
func (m Model) BillingMode() BillingMode {
	if m.Free() {
		return BillingLocal
	}
	if info, ok := planProviders[m.Provider]; ok {
		if info.CreditUnit == "subscription" {
			return BillingOAuthPlan
		}
		return BillingCreditPlan
	}
	if info, ok := ByID(m.Provider); ok && info.OAuth {
		return BillingOAuthPlan
	}
	if m.PriceVaries() {
		return BillingUnknown
	}
	if m.Pricing().Known() {
		return BillingAPIKey
	}
	return BillingUnknown
}

// BillingModeFor resolves the billing mode for a model id and provider id that
// may not be in the catalogue, which is the shape an internal task model takes
// when it was configured by hand. An id the catalogue knows decides from the
// catalogue entry; anything else decides from the provider alone, and a
// provider nobody recognises is unknown rather than a guess.
func BillingModeFor(modelID, providerID string) BillingMode {
	if model, ok := ModelByID(strings.TrimSpace(modelID)); ok {
		return model.BillingMode()
	}
	model := Model{ID: strings.TrimSpace(modelID), Provider: strings.TrimSpace(providerID)}
	if model.Provider == "" {
		return BillingUnknown
	}
	if model.Free() {
		return BillingLocal
	}
	if info, ok := planProviders[model.Provider]; ok {
		if info.CreditUnit == "subscription" {
			return BillingOAuthPlan
		}
		return BillingCreditPlan
	}
	if info, ok := ByID(model.Provider); ok && info.OAuth {
		return BillingOAuthPlan
	}
	return BillingUnknown
}
