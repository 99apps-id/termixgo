package cron

import (
	"bufio"
	"encoding/json"
	"fmt"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/rotate"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// HistoryEntry is one scheduled run or delivery, recorded for `cron runs`.
type HistoryEntry struct {
	Time   time.Time `json:"ts"`
	Kind   string    `json:"kind"`
	Job    string    `json:"job,omitempty"`
	Name   string    `json:"name,omitempty"`
	Agent  string    `json:"agent,omitempty"`
	Target string    `json:"target,omitempty"`
	OK     bool      `json:"ok"`
	Error  string    `json:"error,omitempty"`
	Millis int64     `json:"ms,omitempty"`
}

// HistoryPath is where scheduled-run history lives.
func HistoryPath() (string, error) {
	return config.HomePath("cron-history.jsonl")
}

// RecordHistory appends one entry. A failed write is not fatal: the run itself
// already happened.
func RecordHistory(entry HistoryEntry) {
	path, err := HistoryPath()
	if err != nil {
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if err := rotate.Append(path, data, rotate.Options{}); err != nil {
		return
	}
	_ = secrets.RestrictFile(path)
}

// ReadHistory returns the newest limit entries, optionally for one job id.
func ReadHistory(limit int, jobID string) ([]HistoryEntry, error) {
	path, err := HistoryPath()
	if err != nil {
		return nil, err
	}
	reader, err := rotate.Reader(path)
	if err != nil {
		return nil, fmt.Errorf("read cron history: %w", err)
	}
	defer reader.Close()
	var entries []HistoryEntry
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry HistoryEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if jobID != "" && entry.Job != jobID {
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read cron history: %w", err)
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}
