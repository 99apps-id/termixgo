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

func TestChannelHookCommand(t *testing.T) {
	withState(t)

	cfg := config.Default()
	cfg = cfg.WithWhatsAppAccount("default", config.ChannelAccount{
		Agent: "default",
	})
	if err := config.Save(cfg); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	var buf bytes.Buffer
	// Configure hook
	if err := runChannel([]string{"hook", "whatsapp", "default", "--url", "http://localhost:8080/api/bot/handle", "--secret", "sec123"}, &buf); err != nil {
		t.Fatalf("runChannel hook configure: %v", err)
	}
	if !strings.Contains(buf.String(), "http://localhost:8080/api/bot/handle") {
		t.Errorf("expected configured hook url in output, got: %q", buf.String())
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	acct, ok := reloaded.WhatsAppAccount("default")
	if !ok || acct.HookURL != "http://localhost:8080/api/bot/handle" || acct.HookSecret != "sec123" {
		t.Fatalf("account hook not persisted properly: %#v", acct)
	}

	// Query hook
	buf.Reset()
	if err := runChannel([]string{"hook", "whatsapp", "default"}, &buf); err != nil {
		t.Fatalf("runChannel hook query: %v", err)
	}
	if !strings.Contains(buf.String(), "http://localhost:8080/api/bot/handle") {
		t.Errorf("query should display url, got: %q", buf.String())
	}

	// Clear hook
	buf.Reset()
	if err := runChannel([]string{"hook", "whatsapp", "default", "--clear"}, &buf); err != nil {
		t.Fatalf("runChannel hook clear: %v", err)
	}
	if !strings.Contains(buf.String(), "cleared hook") {
		t.Errorf("expected cleared hook message, got: %q", buf.String())
	}

	reloaded, _ = config.Load()
	acct, _ = reloaded.WhatsAppAccount("default")
	if acct.HookURL != "" || acct.HookSecret != "" {
		t.Errorf("expected empty hook after clear, got: %#v", acct)
	}
}
