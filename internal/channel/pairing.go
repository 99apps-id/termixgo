package channel

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// PairingFileName is the pairing file inside the state directory.
const PairingFileName = "pairing.json"

// PairingRequest is one unknown sender waiting for the operator's answer.
type PairingRequest struct {
	Channel    string    `json:"channel"`
	Account    string    `json:"account"`
	SenderID   string    `json:"senderId"`
	SenderName string    `json:"senderName,omitempty"`
	Code       string    `json:"code"`
	CreatedAt  time.Time `json:"createdAt"`
}

const (
	// PairingTTL is how long an unanswered request stays open. A request an
	// operator never looked at is stale: the sender has moved on, and the file
	// would otherwise grow for as long as strangers keep writing.
	PairingTTL = 24 * time.Hour
	// MaxPendingPairings caps the open requests. Past it a new stranger gets no
	// code, so a flood of senders cannot fill the disk or bury the real request.
	MaxPendingPairings = 50
)

// DefaultPairingPath is where pairing requests live.
func DefaultPairingPath() (string, error) {
	return config.HomePath(PairingFileName)
}

// PairingStore remembers pending direct-message pairing requests: an unknown
// sender receives a short code and waits for the operator to approve it from a
// terminal. Approving adds the sender to that account's allowlist.
type PairingStore struct {
	mu       sync.Mutex
	path     string
	requests []PairingRequest
}

// OpenPairingStore loads the store, treating a missing file as empty. The file
// carries sender ids, so it is written 0600 like the other private state.
func OpenPairingStore(path string) (*PairingStore, error) {
	store := &PairingStore{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pairing requests: %w", err)
	}
	shape := struct {
		Requests []PairingRequest `json:"requests"`
	}{}
	if err := json.Unmarshal(data, &shape); err != nil {
		// A bare array is what an older hand edit may hold; accept it too.
		if arrayErr := json.Unmarshal(data, &store.requests); arrayErr != nil {
			return nil, fmt.Errorf("parse pairing requests %s: %w", path, err)
		}
		return store, nil
	}
	store.requests = shape.Requests
	return store, nil
}

// Request returns the pending request for one sender, creating it with a fresh
// code on first contact. Asking twice returns the same code instead of
// flooding the sender with new ones; created reports whether this call is what
// opened the request.
func (s *PairingStore) Request(channelName, account, senderID, senderName string) (PairingRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	for _, existing := range s.requests {
		if existing.Channel == channelName && existing.Account == account && CanonicalID(existing.SenderID) == CanonicalID(senderID) {
			return existing, false
		}
	}
	if len(s.requests) >= MaxPendingPairings {
		// Nothing is stored and nothing is promised: the caller still gets a
		// request to show, but it will never be approvable.
		return PairingRequest{Channel: channelName, Account: account, SenderID: senderID, Code: "------"}, false
	}
	request := PairingRequest{
		Channel:    strings.TrimSpace(channelName),
		Account:    strings.TrimSpace(account),
		SenderID:   strings.TrimSpace(senderID),
		SenderName: strings.TrimSpace(senderName),
		Code:       pairingCode(),
		CreatedAt:  time.Now(),
	}
	s.requests = append(s.requests, request)
	_ = s.saveLocked()
	return request, true
}

// pruneLocked drops the requests older than PairingTTL.
func (s *PairingStore) pruneLocked(now time.Time) {
	kept := s.requests[:0]
	for _, request := range s.requests {
		if request.CreatedAt.IsZero() || now.Sub(request.CreatedAt) < PairingTTL {
			kept = append(kept, request)
		}
	}
	if len(kept) != len(s.requests) {
		s.requests = kept
		_ = s.saveLocked()
	}
}

// Pending lists the open requests, oldest first.
func (s *PairingStore) Pending() []PairingRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	out := append([]PairingRequest{}, s.requests...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Approve removes a request by code and returns it, so the caller can add the
// sender to the account's allowlist.
func (s *PairingStore) Approve(code string) (PairingRequest, bool) {
	return s.take(code)
}

// Deny removes a request by code without approving the sender.
func (s *PairingStore) Deny(code string) (PairingRequest, bool) {
	return s.take(code)
}

func (s *PairingStore) take(code string) (PairingRequest, bool) {
	needle := strings.TrimSpace(code)
	if needle == "" {
		return PairingRequest{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	for index, request := range s.requests {
		if !strings.EqualFold(request.Code, needle) {
			continue
		}
		s.requests = append(append([]PairingRequest{}, s.requests[:index]...), s.requests[index+1:]...)
		_ = s.saveLocked()
		return request, true
	}
	return PairingRequest{}, false
}

// saveLocked persists the store. It is best effort at the call sites: a failed
// write costs a repeated pairing prompt, not a lost conversation.
func (s *PairingStore) saveLocked() error {
	if strings.TrimSpace(s.path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	data, err := json.MarshalIndent(struct {
		Requests []PairingRequest `json:"requests"`
	}{Requests: s.requests}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode pairing requests: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write pairing requests: %w", err)
	}
	if err := secrets.RestrictFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("protect pairing requests: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace pairing requests: %w", err)
	}
	return nil
}

// pairingCode returns a zero-padded six-digit code.
func pairingCode() string {
	limit := big.NewInt(1000000)
	value, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
	}
	return fmt.Sprintf("%06d", value.Int64())
}
