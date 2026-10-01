package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecordAndTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	ledger, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for index := 0; index < 3; index++ {
		if err := ledger.Record(Entry{Kind: "tool", Name: "read_file", OK: true, Millis: int64(index)}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	last, err := ledger.Tail(1)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(last) != 1 || last[0].Name != "read_file" || last[0].Millis != 2 {
		t.Fatalf("Tail(1) = %v", last)
	}
	all, err := ledger.Tail(0)
	if err != nil || len(all) != 3 {
		t.Fatalf("Tail(0) = %v, %v", all, err)
	}
	if !all[0].Time.Before(all[2].Time.Add(time.Second)) {
		t.Errorf("entries should carry a timestamp")
	}
}

func TestTailOnAMissingLedgerIsEmpty(t *testing.T) {
	ledger, err := Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	entries, err := ledger.Tail(10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a missing ledger should be empty, got %v", entries)
	}
}

// TestLedgerIsMetadataOnly pins that the file holds no free text: an entry has
// no field for a prompt or a result, only the fixed schema.
func TestLedgerIsMetadataOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	ledger, _ := Open(path)
	if err := ledger.Record(Entry{Kind: "turn", Name: "run", OK: false, StopReason: "error"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt", "result", "text"} {
		if strings.Contains(string(data), "\""+forbidden+"\"") {
			t.Errorf("the ledger should not carry a %q field: %s", forbidden, data)
		}
	}
}
