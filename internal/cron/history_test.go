package cron

import (
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestHistoryRoundTripAndFilter(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	RecordHistory(HistoryEntry{Time: time.Now(), Kind: "run", Job: "a", OK: true, Millis: 12})
	RecordHistory(HistoryEntry{Time: time.Now(), Kind: "deliver", Job: "b", OK: false, Error: "boom"})

	all, err := ReadHistory(0, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %+v, err %v", all, err)
	}
	onlyA, err := ReadHistory(10, "a")
	if err != nil || len(onlyA) != 1 || onlyA[0].Job != "a" {
		t.Fatalf("onlyA = %+v, err %v", onlyA, err)
	}
	lastOnly, err := ReadHistory(1, "")
	if err != nil || len(lastOnly) != 1 || lastOnly[0].Job != "b" {
		t.Fatalf("lastOnly = %+v, err %v", lastOnly, err)
	}
}
