package telegram

import (
	"path/filepath"
	"testing"
)

// TestFinishUpdatePersistsOnlyContiguousWatermark pins the crash-safety rule:
// the persisted offset advances only past updates that have all finished, so a
// crash never drops a prompt whose handler was still running.
func TestFinishUpdatePersistsOnlyContiguousWatermark(t *testing.T) {
	bot := New("1:a", nil)
	bot.SetCursorPath(filepath.Join(t.TempDir(), "offset.txt"))
	bot.offset = 8
	bot.persisted = 5
	bot.inflight = map[int64]bool{5: true, 6: true, 7: true}

	// 7 finishes first: the watermark cannot pass the still-running 5.
	bot.finishUpdate(7)
	if got := bot.loadOffset(); got != 5 {
		t.Fatalf("offset after update 7 = %d, want 5", got)
	}

	// 6 finishes: still 5.
	bot.finishUpdate(6)
	if got := bot.loadOffset(); got != 5 {
		t.Fatalf("offset after update 6 = %d, want 5", got)
	}

	// 5 finishes: 5, 6 and 7 are all done, so the watermark reaches 8.
	bot.finishUpdate(5)
	if got := bot.loadOffset(); got != 8 {
		t.Fatalf("offset after update 5 = %d, want 8", got)
	}
}
