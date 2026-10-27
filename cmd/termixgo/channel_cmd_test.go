package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestChannelDMCommand(t *testing.T) {
	withState(t)

	cfg := config.Default()
	cfg = cfg.WithWhatsAppAccount("default", config.ChannelAccount{
		Agent:    "default",
		DMPolicy: config.DMPolicyAllowlist,
	})
	if err := config.Save(cfg); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	var buf bytes.Buffer
	if err := runChannel([]string{"dm", "whatsapp", "default", "open"}, &buf); err != nil {
		t.Fatalf("runChannel dm: %v", err)
	}
	if !strings.Contains(buf.String(), "policy set to open") {
		t.Errorf("unexpected output: %q", buf.String())
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	account, ok := reloaded.WhatsAppAccount("default")
	if !ok {
		t.Fatal("whatsapp account not found")
	}
	if account.DMPolicy != config.DMPolicyOpen {
		t.Errorf("expected dmPolicy=open, got %q", account.DMPolicy)
	}

	// Test invalid policy
	buf.Reset()
	if err := runChannel([]string{"dm", "whatsapp", "default", "invalid_policy"}, &buf); err == nil {
		t.Error("expected error for invalid dm policy")
	}
}
