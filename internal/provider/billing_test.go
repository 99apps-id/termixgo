package provider

import "testing"

// TestBillingModePerKindOfModel pins the mode decision the ledger records:
// the mode is the provider's billing shape, not a display guess. No prices
// are asserted here, only which currency a model's tokens are counted in.
func TestBillingModePerKindOfModel(t *testing.T) {
	cases := []struct {
		name  string
		model Model
		want  BillingMode
	}{
		{"local ollama is free", Model{ID: "qwen2.5-coder:latest", Provider: "ollama"}, BillingLocal},
		{"codex is a subscription", Model{ID: "gpt-5.3-codex", Provider: "openai-codex"}, BillingOAuthPlan},
		{"claude oauth is a subscription", Model{ID: "claude-opus-5", Provider: "claude-oauth"}, BillingOAuthPlan},
		{"copilot is a subscription", Model{ID: "gpt-6-astra", Provider: "github-copilot"}, BillingOAuthPlan},
		{"qwen token plan bills credits", Model{ID: "qwen3.8-max", Provider: "qwen-token-plan"}, BillingCreditPlan},
		{"stepfun plan bills credits", Model{ID: "step-3.7-flash", Provider: "stepfun-plan"}, BillingCreditPlan},
		{"priced openai model is pay per token", Model{ID: "gpt-5.5", Provider: "openai"}, BillingAPIKey},
		{"router price varies, so unknown", Model{ID: "openrouter/auto", Provider: "openrouter"}, BillingUnknown},
		{"unpriced model is unknown, not free", Model{ID: "no-such-model", Provider: "openai"}, BillingUnknown},
	}
	for _, testCase := range cases {
		if got := testCase.model.BillingMode(); got != testCase.want {
			t.Errorf("%s: BillingMode = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

// TestBillingModeForUncataloguedIDs covers the internal-task shape: a model
// id resolved from a wire name the catalogue does not list still gets the
// mode its provider implies, and no provider means unknown.
func TestBillingModeForUncataloguedIDs(t *testing.T) {
	if got := BillingModeFor("some-wire-model", "claude-oauth"); got != BillingOAuthPlan {
		t.Errorf("BillingModeFor wire model on oauth provider = %q, want %q", got, BillingOAuthPlan)
	}
	if got := BillingModeFor("some-wire-model", ""); got != BillingUnknown {
		t.Errorf("BillingModeFor with no provider = %q, want %q", got, BillingUnknown)
	}
	if got := BillingModeFor("gpt-5.5", ""); got != BillingAPIKey {
		t.Errorf("BillingModeFor catalogued id = %q, want %q", got, BillingAPIKey)
	}
}

func TestBillingModeAuthTypeHelpers(t *testing.T) {
	if BillingAPIKey.AuthType() != AuthBYOK || !BillingAPIKey.IsBYOK() || BillingAPIKey.IsOAuth() {
		t.Errorf("BillingAPIKey should be BYOK")
	}
	if BillingOAuthPlan.AuthType() != AuthOAuth || BillingOAuthPlan.IsBYOK() || !BillingOAuthPlan.IsOAuth() {
		t.Errorf("BillingOAuthPlan should be OAuth")
	}
	if BillingCreditPlan.AuthType() != AuthCredit {
		t.Errorf("BillingCreditPlan should be Credit")
	}
	if BillingLocal.AuthType() != AuthLocal {
		t.Errorf("BillingLocal should be Local")
	}
	if BillingUnknown.AuthType() != AuthUnknown {
		t.Errorf("BillingUnknown should be Unknown")
	}
}
