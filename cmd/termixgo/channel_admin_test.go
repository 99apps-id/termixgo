package main

import (
	"io"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestWhatsAppAdminCLIUsesAccountAndIdentityType(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default().WithWhatsAppAccount("sales", config.ChannelAccount{Enabled: true})
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	edit := func(action, id string) error {
		return runChannel([]string{"admin", action, "whatsapp", "sales", id}, io.Discard)
	}
	for _, id := range []string{"+628123", "628123", "628123@lid"} {
		if err := edit("add", id); err != nil {
			t.Fatal(err)
		}
	}
	if err := edit("rm", "628123"); err != nil {
		t.Fatal(err)
	}
	if err := edit("typo", "628999"); err == nil {
		t.Fatal("unknown admin action was accepted")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	account, ok := cfg.WhatsAppAccount("sales")
	if !ok || len(account.Admins) != 1 || account.Admins[0] != "628123@lid" {
		t.Fatal("only the explicitly granted LID should remain after removing the phone")
	}
}
