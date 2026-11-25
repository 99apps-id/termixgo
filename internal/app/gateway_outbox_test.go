package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestGatewayOutboxPersistsAndDeduplicatesTransitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.sqlite")
	outbox, err := openGatewayOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	targets := []gatewayAdminTarget{{channel: config.ChannelTelegram, account: "main", user: "123"}, {channel: config.ChannelWhatsApp, account: "wa", user: "62812345@s.whatsapp.net"}}
	if emitted, err := outbox.transition("whatsapp/wa", "online", targets); err != nil || emitted {
		t.Fatalf("initial online emitted=%v err=%v", emitted, err)
	}
	if emitted, err := outbox.transition("whatsapp/wa", "offline", targets); err != nil || !emitted {
		t.Fatalf("offline emitted=%v err=%v", emitted, err)
	}
	if emitted, err := outbox.transition("whatsapp/wa", "offline", targets); err != nil || emitted {
		t.Fatalf("duplicate emitted=%v err=%v", emitted, err)
	}
	if err := outbox.db.Close(); err != nil {
		t.Fatal(err)
	}
	outbox, err = openGatewayOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.db.Close()
	pending, err := outbox.pending()
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	if err := outbox.ack(pending[0].id); err != nil {
		t.Fatal(err)
	}
	if emitted, err := outbox.transition("whatsapp/wa", "online", targets); err != nil || !emitted {
		t.Fatalf("recovery emitted=%v err=%v", emitted, err)
	}
	pending, err = outbox.pending()
	if err != nil || len(pending) != 3 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
}

func TestGatewayOutboxDoesNotCommitWithoutAdmins(t *testing.T) {
	outbox, err := openGatewayOutbox(filepath.Join(t.TempDir(), "gateway.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.db.Close()
	if _, err := outbox.transition("whatsapp/wa", "online", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.transition("whatsapp/wa", "offline", nil); err == nil {
		t.Fatal("expected missing admin error")
	}
	var status string
	if err := outbox.db.QueryRow(`SELECT status FROM gateway_state WHERE source=?`, "whatsapp/wa").Scan(&status); err != nil || status != "online" {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

// The alert names the kind of gateway and never the account behind it.
func TestGatewayAlertTextOmitsTheAccountName(t *testing.T) {
	outbox, err := openGatewayOutbox(filepath.Join(t.TempDir(), "alerts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.db.Close()
	targets := []gatewayAdminTarget{{channel: "telegram", account: "default", user: "42"}}
	if _, err := outbox.transition("whatsapp/raka", "online", targets); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"offline", "online"} {
		if emitted, err := outbox.transition("whatsapp/raka", status, targets); err != nil || !emitted {
			t.Fatalf("%s: emitted=%v err=%v", status, emitted, err)
		}
	}
	deliveries, err := outbox.pending()
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("deliveries=%v err=%v", deliveries, err)
	}
	for _, delivery := range deliveries {
		if strings.Contains(strings.ToLower(delivery.text), "raka") || !strings.Contains(delivery.text, "WhatsApp") {
			t.Errorf("alert text = %q", delivery.text)
		}
	}
	if gatewayLabel("telegram/x") != "Telegram" || gatewayLabel("other/x") != "other" {
		t.Errorf("labels = %q %q", gatewayLabel("telegram/x"), gatewayLabel("other/x"))
	}
}

// A restart is announced once, with its cause, when the gateway is back.
func TestGatewayRestartIsAnnouncedOnceWithItsCause(t *testing.T) {
	cases := []struct {
		name       string
		oldVersion string
		oldBoot    int64
		version    string
		boot       int64
		want       string
	}{
		{"update", "0.2.12", 1000, "0.2.13", 1000, "because of an update from version 0.2.12 to 0.2.13"},
		{"vps", "0.2.12", 1000, "0.2.12", 90000, "because the VPS restarted"},
		{"service", "0.2.12", 1000, "0.2.12", 1000, "because the service restarted"},
		{"same boot within jitter", "0.2.12", 1000, "0.2.12", 1060, "because the service restarted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outbox, err := openGatewayOutbox(filepath.Join(t.TempDir(), "alerts.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer outbox.db.Close()
			targets := []gatewayAdminTarget{{channel: "telegram", account: "default", user: "42"}}
			if err := outbox.beginRun(tc.oldVersion, tc.oldBoot); err != nil {
				t.Fatal(err)
			}
			for _, source := range []string{"whatsapp/a", "telegram/b"} {
				outbox.transition(source, "starting", targets)
				outbox.transition(source, "online", targets)
			}
			if pending, _ := outbox.pending(); len(pending) != 0 {
				t.Fatalf("the first run alerted: %v", pending)
			}
			if err := outbox.beginRun(tc.version, tc.boot); err != nil {
				t.Fatal(err)
			}
			for _, source := range []string{"whatsapp/a", "telegram/b"} {
				if emitted, err := outbox.transition(source, "starting", targets); err != nil || emitted {
					t.Fatalf("a start alone must not alert: emitted=%v err=%v", emitted, err)
				}
			}
			outbox.transition("whatsapp/a", "online", targets)
			outbox.transition("telegram/b", "online", targets)
			pending, err := outbox.pending()
			if err != nil || len(pending) != 1 {
				t.Fatalf("a restart must be announced once: %v (err=%v)", pending, err)
			}
			if !strings.Contains(pending[0].text, tc.want) || !strings.Contains(pending[0].text, "restarted") {
				t.Errorf("text = %q, want the cause %q", pending[0].text, tc.want)
			}
		})
	}
}
