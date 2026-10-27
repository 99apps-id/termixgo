package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestAgentOptionsComeFromConfig(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	cfg = cfg.WithAgent("sales", config.Agent{})
	cfg.ActiveAgent = "sales"
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	options := SlashOptions("/agent")
	if len(options) != 2 {
		t.Fatalf("options = %+v, want one per configured agent", options)
	}
	if options[0].Value != config.FinalAgentName || options[1].Value != "sales" {
		t.Errorf("options must be sorted with the default first: %+v", options)
	}
	if options[1].Detail != "active agent" {
		t.Errorf("the active agent must be marked in the menu: %+v", options[1])
	}
}

func TestSlashAgentListsAndSwitches(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	cfg = cfg.WithAgent("sales", config.Agent{})
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	application := testApp(t)
	model := New(application)
	resize(model, 120, 40)

	model, listing := runSlash(t, model, "/agent")
	if !strings.Contains(listing, "Agents:") || !strings.Contains(listing, "sales") {
		t.Errorf("the listing is missing the registry:\n%s", listing)
	}

	model, _ = runSlash(t, model, "/agent sales")
	if application.ActiveAgent() != "sales" {
		t.Fatalf("ActiveAgent = %q, want sales", application.ActiveAgent())
	}
	if !strings.Contains(allText(model), "Agent is now") {
		t.Errorf("the switch must be reported:\n%s", allText(model))
	}
}
