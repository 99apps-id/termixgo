// Package audit records a metadata-only ledger of what the agent did: one line
// per finished turn or tool call, with result and timing. It never stores a
// prompt, a tool result or a secret, so the ledger is safe to keep and to share
// when something needs explaining.
package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

// Entry is one audited action. Fields are deliberately coarse: a name, a
// result and a timing, never the content that flowed through.
type Entry struct {
	Time         time.Time `json:"ts"`
	Workspace    string    `json:"workspace,omitempty"`
	Kind         string    `json:"kind"`
	Name         string    `json:"name,omitempty"`
	OK           bool      `json:"ok"`
	Millis       int64     `json:"ms,omitempty"`
	StopReason   string    `json:"stop,omitempty"`
	InputTokens  int       `json:"in,omitempty"`
	OutputTokens int       `json:"out,omitempty"`
}

// DefaultPath is where the ledger lives: ~/.termixgo/audit.jsonl.
func DefaultPath() (string, error) {
	return config.HomePath("audit.jsonl")
}

// Ledger appends entries to a JSONL file.
type Ledger struct {
	mu   sync.Mutex
	path string
}

// Open prepares a ledger at path. It does not create the file until the first
// entry is written.
func Open(path string) (*Ledger, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("the audit path is empty")
	}
	return &Ledger{path: path}, nil
}

// Path reports the ledger file.
func (l *Ledger) Path() string { return l.path }

// Record appends one entry. A failure to write is returned so the caller can
// decide whether it matters; the agent never fails a turn over the ledger.
func (l *Ledger) Record(entry Entry) error {
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return fmt.Errorf("create audit directory: %w", err)
	}
	file, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	return nil
}

// Tail returns the newest entries, up to limit, oldest first. A missing file is
// an empty ledger, not an error.
func (l *Ledger) Tail(limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 50
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	file, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	defer file.Close()
	ring := make([]Entry, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if len(ring) == limit {
			copy(ring, ring[1:])
			ring = ring[:limit-1]
		}
		ring = append(ring, entry)
	}
	return ring, scanner.Err()
}
