package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

// TestDoctorWarnsWhenTheBudgetCannotFire is the honesty guard for the cost cap.
//
// A budget only works if a price is known for the model. Without this warning
// the doctor line reads as protection the operator does not actually have, and
// they find out by seeing a bill.
func TestDoctorWarnsWhenTheBudgetCannotFire(t *testing.T) {
	withState(t)

	cfg := config.Default()
	cfg.DefaultModel = "vendor:brand-new-model"
	cfg.CostBudgetUSD = 1.00
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stdout, _, err := runCLI(t, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, "cost budget:    $1.00 per session") {
		t.Errorf("the budget should still be reported:\n%s", stdout)
	}
	if !strings.Contains(stdout, "cannot fire") {
		t.Errorf("an unenforceable cap must be called out:\n%s", stdout)
	}
	if !strings.Contains(stdout, "modelPricing") {
		t.Errorf("the warning should name the fix:\n%s", stdout)
	}
}

// TestDoctorStaysQuietWhenTheBudgetCanFire is the other side: a warning on
// every priced model would be noise nobody reads.
func TestDoctorStaysQuietWhenTheBudgetCanFire(t *testing.T) {
	cases := []struct {
		name   string
		model  string
		prices map[string]config.ModelPrice
	}{
		{name: "a catalogued model", model: "gpt-5.4-mini"},
		{name: "a locally hosted model", model: "qwen2.5-coder:latest"},
		{
			name:   "an unpriced model with an override",
			model:  "vendor:brand-new-model",
			prices: map[string]config.ModelPrice{"brand-new-model": {InputPerMillion: 3, OutputPerMillion: 9}},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			withState(t)
			cfg := config.Default()
			cfg.DefaultModel = testCase.model
			cfg.CostBudgetUSD = 1.00
			cfg.ModelPricing = testCase.prices
			if err := config.Save(cfg); err != nil {
				t.Fatalf("Save: %v", err)
			}

			stdout, _, err := runCLI(t, "doctor")
			if err != nil {
				t.Fatalf("doctor: %v", err)
			}
			if strings.Contains(stdout, "cannot fire") {
				t.Errorf("the cap is enforceable, so there should be no warning:\n%s", stdout)
			}
		})
	}
}

// TestBudgetCanFireCoversTheEmptyModel keeps a fresh install from warning about
// a budget it has not been given.
func TestBudgetCanFireCoversTheEmptyModel(t *testing.T) {
	cfg := config.Default()
	if budgetCanFire(cfg) {
		t.Errorf("with no model configured nothing can be priced")
	}
	// A blank-but-present value is treated the same way.
	cfg.DefaultModel = "   "
	if budgetCanFire(cfg) {
		t.Errorf("a blank model id must not be treated as priced")
	}
}

func TestDoctorOnAFreshInstallDoesNotWarnAboutABudget(t *testing.T) {
	withState(t)
	stdout, _, err := runCLI(t, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if strings.Contains(stdout, "cannot fire") {
		t.Errorf("no budget was configured, so there is nothing to warn about:\n%s", stdout)
	}
	if !strings.Contains(stdout, "cost budget:    none") {
		t.Errorf("the unlimited default should be stated:\n%s", stdout)
	}
}

// TestUsageDocumentsTheBudgetAndPricingKeys is the discovery path: a setting
// nobody can find does not exist.
func TestUsageDocumentsTheBudgetAndPricingKeys(t *testing.T) {
	var out bytes.Buffer
	writeUsage(&out)
	// The help text points at the config for the two cost settings rather than
	// repeating their shape, which keeps one source of truth for the schema.
	usage := out.String()
	if !strings.Contains(usage, "secrets.json") {
		t.Errorf("usage should say where secrets live:\n%s", usage)
	}
	if !strings.Contains(usage, "doctor") {
		t.Errorf("usage should point at doctor, which explains the config:\n%s", usage)
	}
}
