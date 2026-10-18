package app

import "testing"

// TestUnpairInvalidatesThePairingCode closes the reuse hole: after the operator
// unpairs, the code that was live must not still be accepted, so a leaked code
// cannot re-pair the bot later.
func TestUnpairInvalidatesThePairingCode(t *testing.T) {
	application := newTestApp(t)

	before, err := application.EnsurePairingCode()
	if err != nil {
		t.Fatalf("EnsurePairingCode: %v", err)
	}
	if before == "" {
		t.Fatal("a fresh pairing code should not be empty")
	}

	// Pair, then unpair.
	if err := application.SetTelegramChat(42, 7); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if err := application.SetTelegramChat(0, 0); err != nil {
		t.Fatalf("unpair: %v", err)
	}

	if code := application.Config().Telegram.PairingCode; code != "" {
		t.Errorf("unpair must drop the old code, got %q", code)
	}
	after, err := application.EnsurePairingCode()
	if err != nil {
		t.Fatalf("EnsurePairingCode after unpair: %v", err)
	}
	if after == "" {
		t.Fatal("a fresh code should be available for the next pairing")
	}
}
