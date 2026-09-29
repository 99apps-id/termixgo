package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// unpricedModel builds a model for a vendor model nobody has priced, which is
// the state a brand-new or self-hosted endpoint is in.
func unpricedModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())

	cfg := config.Default()
	cfg.DefaultModel = "brand-new-model"
	cfg.ApprovalMode = config.ApprovalAll
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	application := testApp(t)
	if err := application.Secrets().Set(secrets.ProviderKey("openai"), "sk-test"); err != nil {
		t.Fatalf("store key: %v", err)
	}
	// The provider is chosen by the stored key, so the model resolves to a real
	// client without any network access.
	if _, err := application.SetModelByQuery("brand-new-model"); err != nil {
		t.Fatalf("SetModelByQuery: %v", err)
	}
	if _, known := application.Cost(); known {
		t.Fatalf("the fixture must be unpriced")
	}

	model := New(application)
	resize(model, 120, 40)
	return model
}

// TestCostCommandCallsOutAnUnenforceableBudget is the honesty guard in the TUI.
//
// The operator set a cap and the model has no price, so the cap can never fire.
// Reporting "budget $1.00" on its own would read as protection they do not
// have, which they would only discover from a bill.
func TestCostCommandCallsOutAnUnenforceableBudget(t *testing.T) {
	model := unpricedModel(t)
	if err := model.app.UpdateConfig(func(cfg *config.Config) { cfg.CostBudgetUSD = 1.00 }); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	next, _ := model.runSlash("cost", "")
	view := allText(next.(*Model))

	if !strings.Contains(view, "cost unknown") {
		t.Errorf("/cost should say the spend cannot be measured:\n%s", view)
	}
	if !strings.Contains(view, "budget $1.00") {
		t.Errorf("/cost should report the configured cap:\n%s", view)
	}
	if !strings.Contains(view, "cannot be enforced") {
		t.Errorf("an unenforceable cap must be called out:\n%s", view)
	}
	if !strings.Contains(view, "modelPricing") {
		t.Errorf("the message should name the fix:\n%s", view)
	}
}

// TestCostCommandStaysQuietAboutAnEnforceableBudget is the other side: adding
// the caveat to a model that is priced would train the operator to ignore it.
func TestCostCommandStaysQuietAboutAnEnforceableBudget(t *testing.T) {
	model := chatModel(t)
	if err := model.app.UpdateConfig(func(cfg *config.Config) { cfg.CostBudgetUSD = 1.00 }); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	next, _ := model.runSlash("cost", "")
	view := allText(next.(*Model))

	if strings.Contains(view, "cannot be enforced") {
		t.Errorf("a locally hosted model is known-free, so the cap is measurable:\n%s", view)
	}
	if !strings.Contains(view, "budget $1.00") {
		t.Errorf("/cost should still report the cap:\n%s", view)
	}
}

func TestCostCommandWithoutABudgetSaysSo(t *testing.T) {
	model := chatModel(t)
	next, _ := model.runSlash("cost", "")
	if view := allText(next.(*Model)); !strings.Contains(view, "no budget set") {
		t.Errorf("/cost should report the absence of a cap:\n%s", view)
	}
}
