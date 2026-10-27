package app

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestStartWhatsAppRefusesAnUnlinkedAccount(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg = cfg.WithWhatsAppAccount("sales", config.ChannelAccount{
		Agent:   "sales",
		Enabled: true,
	})
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = application.StartChannel(config.ChannelWhatsApp, "sales")
	if err == nil {
		t.Fatalf("an unlinked account must not start")
	}
	if !strings.Contains(err.Error(), "not linked") {
		t.Errorf("the refusal must name the next step, got %v", err)
	}
	if statuses := application.ChannelStatuses(); len(statuses) == 0 {
		t.Errorf("the account should still be listed in the status view")
	}
}
