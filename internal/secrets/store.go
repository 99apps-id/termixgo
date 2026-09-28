// Package secrets stores the values Termixgo must keep private: provider API
// keys and the Telegram bot token.
//
// The store is a single 0600 JSON file in the state directory, which is the
// same fallback the desktop app uses on Linux. No secret is ever written to
// config.json, a session file or a log.
package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/99apps-id/termixgo/internal/config"
)

// FileName is the secret file name inside the state directory.
const FileName = "secrets.json"

// Well-known key builders. Exported so no caller hand-writes a key string.
const (
	telegramTokenKey = "telegram:token"
	providerPrefix   = "provider:"
)

// ProviderKey is the store key for one provider's API key.
func ProviderKey(provider string) string { return providerPrefix + provider }

// TelegramTokenKey is the store key for the bot token.
func TelegramTokenKey() string { return telegramTokenKey }

// Store is a concurrency-safe secret map backed by a private file.
type Store struct {
	mu   sync.Mutex
	path string
	data map[string]string
	// protectionErr records a failure to restrict the file's access rules.
	// It is reported rather than fatal: a read-only share can refuse the
	// change, and locking the operator out of their own keys would be worse
	// than telling them about it.
	protectionErr error
}

// ProtectionError reports the last failure to restrict the secret file's
// permissions, or nil when the file is private. `termixgo doctor` surfaces it.
func (s *Store) ProtectionError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.protectionErr
}

func (s *Store) setProtectionErr(err error) {
	s.mu.Lock()
	s.protectionErr = err
	s.mu.Unlock()
}

// Path returns the secret file location, which callers need to audit it.
func (s *Store) Path() string { return s.path }

// Load reads the secret file. A missing file yields an empty store.
func Load() (*Store, error) {
	path, err := config.HomePath(FileName)
	if err != nil {
		return nil, err
	}
	store := &Store{path: path, data: map[string]string{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read secrets: %w", err)
	}
	if err := json.Unmarshal(raw, &store.data); err != nil {
		return nil, fmt.Errorf("parse secrets %s: %w", path, err)
	}
	if store.data == nil {
		store.data = map[string]string{}
	}
	// Repair the access rules of a file written by an older build, or under a
	// looser umask. A failure is recorded rather than returned so the
	// operator is warned by doctor instead of being locked out.
	if hardenErr := restrictFile(path); hardenErr != nil {
		store.protectionErr = hardenErr
	}
	return store, nil
}

// Get returns a secret or the empty string.
func (s *Store) Get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key]
}

// Has reports whether a non-empty secret is stored.
func (s *Store) Has(key string) bool { return strings.TrimSpace(s.Get(key)) != "" }

// Set writes a secret and persists the file at 0600.
func (s *Store) Set(key, value string) error {
	s.mu.Lock()
	previous, existed := s.data[key]
	s.data[key] = value
	s.mu.Unlock()

	if err := s.save(); err != nil {
		s.mu.Lock()
		if existed {
			s.data[key] = previous
		} else {
			delete(s.data, key)
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

// Delete removes a secret. Deleting a missing key is not an error.
func (s *Store) Delete(key string) error {
	s.mu.Lock()
	if _, ok := s.data[key]; !ok {
		s.mu.Unlock()
		return nil
	}
	delete(s.data, key)
	s.mu.Unlock()
	return s.save()
}

// Keys lists stored keys, sorted. It is used by the status view and never
// exposes a value.
func (s *Store) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.data))
	for key := range s.data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *Store) save() error {
	s.mu.Lock()
	encoded, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	// The directory carries the restriction that children inherit, so it is
	// hardened before the file is created inside it.
	if err := restrictDirectory(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("protect state directory: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("write secrets: %w", err)
	}
	if err := restrictFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("protect secrets: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace secrets: %w", err)
	}
	// Renaming can drop a freshly applied ACL on some filesystems, so the
	// final path is hardened once more before the write is reported as done.
	if err := restrictFile(s.path); err != nil {
		return fmt.Errorf("protect secrets: %w", err)
	}
	s.setProtectionErr(nil)
	return nil
}

// Redact renders a secret for display: prefix, dots, suffix. It never reveals
// more than four characters of the value.
func Redact(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "(not set)"
	}
	if len(trimmed) <= 8 {
		return strings.Repeat("*", len(trimmed))
	}
	return trimmed[:4] + strings.Repeat("*", 6) + trimmed[len(trimmed)-2:]
}
