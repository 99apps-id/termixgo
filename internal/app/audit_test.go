package app

import (
	"context"
	"testing"
)

// TestAuditRecordsATurn pins that a finished turn lands in the metadata-only
// ledger.
func TestAuditRecordsATurn(t *testing.T) {
	application, server := readyApp(t)
	defer server.Close()
	defer application.Shutdown()

	if err := application.RunTurn(context.Background(), "hello"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	entries, err := application.AuditTail(20)
	if err != nil {
		t.Fatalf("AuditTail: %v", err)
	}
	found := false
	for _, entry := range entries {
		if entry.Kind == "turn" && entry.Name == "" {
			found = true
		}
	}
	if !found {
		t.Errorf("a finished turn should be audited, got %v", entries)
	}
}
