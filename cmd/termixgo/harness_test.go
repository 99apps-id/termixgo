package main

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
)

// TestHarnessCommandRoundTrip covers the CLI surface for the harness profile: a
// script has to be able to read the active one and switch it.
func TestHarnessCommandRoundTrip(t *testing.T) {
	withState(t)

	stdout, _, err := runCLI(t, "harness")
	if err != nil {
		t.Fatalf("harness get: %v", err)
	}
	if !strings.Contains(stdout, agent.DefaultHarnessProfile) {
		t.Errorf("harness = %q, want the default profile named", stdout)
	}
	for _, profile := range agent.HarnessProfiles() {
		if !strings.Contains(stdout, profile.ID) {
			t.Errorf("the listing is missing %s:\n%s", profile.ID, stdout)
		}
	}

	if _, _, err := runCLI(t, "harness", "autonomous"); err != nil {
		t.Fatalf("harness set: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HarnessProfile != "autonomous" {
		t.Errorf("HarnessProfile = %q, want autonomous", cfg.HarnessProfile)
	}

	// An unknown id must be refused and must not overwrite the stored one.
	_, _, err = runCLI(t, "harness", "nonsense")
	if err == nil {
		t.Fatalf("an unknown harness must be refused")
	}
	if !strings.Contains(err.Error(), "critical") {
		t.Errorf("the error should name the alternatives, got %v", err)
	}
	cfg, _ = config.Load()
	if cfg.HarnessProfile != "autonomous" {
		t.Errorf("a refused change wrote %q", cfg.HarnessProfile)
	}
}
