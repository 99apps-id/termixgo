package app

import (
	"path/filepath"
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
