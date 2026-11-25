package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GuestSessionTTL is the minimum period of inactivity before retention applies.
const GuestSessionTTL = 30 * 24 * time.Hour

// GuestRetentionResult lists session IDs, not attachment or ledger paths.
type GuestRetentionResult struct {
	Candidates []string
	Deleted    []string
}

// RetainGuestSessions scans only the immediate files in the explicitly supplied
// sessions directory. With apply false it is a dry run. Only regular session JSON
// files with unambiguous explicit guest metadata, a matching ID, a valid last
// activity time, and no in-flight turn qualify. Unclassified and malformed
// files are left alone. No attachments, ledgers, or directories are removed.
//
// Apply is intentionally disabled until cleanup can coordinate with concurrent
// session saves. The caller supplies now to make the cutoff explicit and
// testable. This function never defaults to a user directory.
func RetainGuestSessions(dir string, now time.Time, apply bool) (GuestRetentionResult, error) {
	var result GuestRetentionResult
	if strings.TrimSpace(dir) == "" || now.IsZero() {
		return result, fmt.Errorf("retention requires an explicit directory and time")
	}
	if apply {
		return result, fmt.Errorf("guest session deletion is disabled until concurrent saves are coordinated")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return result, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, fmt.Errorf("retention directory must be a real directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return result, err
	}
	cutoff := now.Add(-GuestSessionTTL)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if checkSessionID(id) != nil {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		before, err := os.Lstat(path)
		if err != nil {
			return result, err
		}
		if !before.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return result, err
		}
		if !expiredGuestSession(data, id, cutoff) {
			continue
		}
		result.Candidates = append(result.Candidates, id)
	}
	return result, nil
}

// Decode exact top-level names and reject duplicates, including case variants;
// encoding/json otherwise accepts ambiguous duplicate or case-folded keys.
func expiredGuestSession(data []byte, id string, cutoff time.Time) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return false
	}
	fields := make(map[string]json.RawMessage)
	seen := make(map[string]bool)
	for dec.More() {
		keyToken, err := dec.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[strings.ToLower(key)] {
			return false
		}
		seen[strings.ToLower(key)] = true
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return false
		}
		fields[key] = value
	}
	if tok, err = dec.Token(); err != nil || tok != json.Delim('}') {
		return false
	}
	if _, err = dec.Token(); err != io.EOF {
		return false
	}
	var guest, inFlight bool
	var storedID string
	var updated time.Time
	if json.Unmarshal(fields["guest"], &guest) != nil || !guest ||
		json.Unmarshal(fields["id"], &storedID) != nil || storedID != id ||
		json.Unmarshal(fields["updatedAt"], &updated) != nil || updated.IsZero() || updated.After(cutoff) {
		return false
	}
	if raw, ok := fields["inFlight"]; ok {
		if string(raw) != "false" || json.Unmarshal(raw, &inFlight) != nil || inFlight {
			return false
		}
	}
	return true
}
