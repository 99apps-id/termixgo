package app

import (
	"os"
	"testing"

	"github.com/99apps-id/termixgo/internal/agenthome"
	"github.com/99apps-id/termixgo/internal/config"
)

func TestAddWhatsAppAccountCreatesAgentAndPolicy(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	application := newTestApp(t)

	if err := application.AddWhatsAppAccount("sales"); err != nil {
		t.Fatalf("AddWhatsAppAccount: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entry, ok := cfg.WhatsAppAccount("sales")
	if !ok {
		t.Fatalf("the account was not registered")
	}
	if entry.Enabled {
		t.Errorf("an unlinked account must start disabled")
	}
	if entry.DMPolicy != config.DMPolicyAllowlist || entry.GroupPolicy != config.GroupPolicyMention {
		t.Errorf("unsafe defaults: %+v", entry)
	}
	if entry.AgentName("sales") != "sales" {
		t.Errorf("the account must speak as its own agent")
	}
	if !cfg.HasAgent("sales") {
		t.Fatalf("the agent was not registered")
	}
	paths, err := agenthome.Resolve("sales", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := os.Stat(paths.Soul); err != nil {
		t.Errorf("the persona file was not created: %v", err)
	}
	if !cfg.IsTrusted(paths.Workspace) {
		t.Errorf("the agent workspace must be trusted")
	}
	// A second call is a no-op, which is what a retried wizard needs.
	if err := application.AddWhatsAppAccount("sales"); err != nil {
		t.Errorf("AddWhatsAppAccount is not idempotent: %v", err)
	}
}
