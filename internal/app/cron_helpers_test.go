package app

import (
	"github.com/99apps-id/termixgo/internal/cron"
)

// cronJobForTest builds the minimal job a delivery test needs.
func cronJobForTest(id, target string) cron.Job {
	return cron.Job{ID: id, Name: id, DeliverTo: target, Announce: true}
}

// readCronHistoryForTest reads the recorded history without knowing its path.
func readCronHistoryForTest() ([]cron.HistoryEntry, error) {
	return cron.ReadHistory(0, "")
}
