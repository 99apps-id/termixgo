package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestGuestRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
	old := now.Add(-GuestSessionTTL).Format(time.RFC3339)
	fresh := now.Add(-GuestSessionTTL + time.Second).Format(time.RFC3339)
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	session := func(id, fields string) string { return fmt.Sprintf(`{"id":%q,"updatedAt":%q,%s}`, id, old, fields) }
	write("old.json", session("old", `"guest":true`))
	write("fresh.json", fmt.Sprintf(`{"id":"fresh","updatedAt":%q,"guest":true}`, fresh))
	write("admin.json", session("admin", `"guest":false`))
	write("unknown.json", session("unknown", `"title":"unknown"`))
	write("flight.json", session("flight", `"guest":true,"inFlight":true`))
	write("badflight.json", session("badflight", `"guest":true,"inFlight":null`))
	write("duplicate.json", session("duplicate", `"guest":false,"Guest":true`))
	write("mismatch.json", session("different", `"guest":true`))
	write("invalid.json", `{"guest":true`)
	write("old.ledger", "ledger")
	if err := os.Mkdir(filepath.Join(dir, "attachments"), 0700); err != nil {
		t.Fatal(err)
	}
	write("attachments/file", "attachment")
	if err := os.Symlink(filepath.Join(dir, "old.json"), filepath.Join(dir, "link.json")); err != nil {
		t.Logf("symlink unavailable: %v", err)
	}
	result, err := RetainGuestSessions(dir, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Candidates, []string{"old"}) || len(result.Deleted) != 0 {
		t.Fatalf("dry run: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = RetainGuestSessions(dir, now, true); err == nil {
		t.Fatal("apply must fail until concurrent session saves are coordinated")
	}
	if _, err := os.Stat(filepath.Join(dir, "old.json")); err != nil {
		t.Fatalf("apply must not delete the candidate: %v", err)
	}
	for _, name := range []string{"fresh.json", "admin.json", "unknown.json", "flight.json", "badflight.json", "duplicate.json", "mismatch.json", "invalid.json", "old.ledger", "attachments/file"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s removed: %v", name, err)
		}
	}
	if _, err := RetainGuestSessions("", now, true); err == nil {
		t.Fatal("accepted empty directory")
	}
	if _, err := RetainGuestSessions(dir, time.Time{}, true); err == nil {
		t.Fatal("accepted zero time")
	}
}

func TestGuestRetentionMetadataRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewSession("workspace", "model")
	s.SetStoreDir(dir)
	if s.Guest() {
		t.Fatal("new session must be unclassified")
	}
	s.SetGuest(true)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSessionIn(dir, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Guest() {
		t.Fatal("guest marker lost")
	}
	loaded.SetGuest(false)
	if err := loaded.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadSessionIn(dir, s.ID())
	if err != nil || loaded.Guest() {
		t.Fatalf("guest marker not cleared: %v", err)
	}
}
