package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

// agyIdentity is the release identity of an installed Antigravity CLI: the
// version it reports and the build changelist baked into it. The two travel
// together in the User-Agent every agy call carries.
type agyIdentity struct {
	Version    string `json:"version"`
	Changelist string `json:"changelist"`
}

// agyIdentityLookup returns the identity of the installed CLI. It is a
// variable so tests do not depend on what is installed on the machine.
var agyIdentityLookup = installedAgyIdentity

var (
	versionPattern    = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	changelistPattern = regexp.MustCompile(`^\d{6,12}$`)
)

// agyIdentityCacheFile is the on-disk cache, keyed by the binary it was read
// from, so a new CLI release is read once and then remembered.
type agyIdentityCache struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	ModNanos int64  `json:"modNanos"`
	agyIdentity
}

var agyIdentityMemo struct {
	mu        sync.Mutex
	checkedAt time.Time
	entry     agyIdentityCache
	found     bool
}

// agyIdentityRecheck is how often the binary is looked at again, so an agy
// that updated itself during a long session is noticed without a stat on every
// request.
const agyIdentityRecheck = 30 * time.Second

// installedAgyIdentity reads the identity of the CLI findAgy locates. It
// answers false when there is no CLI or its identity cannot be read in the
// shape a real release has, and the caller then keeps the identity built into
// this version: a guessed pair is worse than a stale one.
func installedAgyIdentity() (agyIdentity, bool) {
	agyIdentityMemo.mu.Lock()
	defer agyIdentityMemo.mu.Unlock()
	if time.Since(agyIdentityMemo.checkedAt) < agyIdentityRecheck && !agyIdentityMemo.checkedAt.IsZero() {
		return agyIdentityMemo.entry.agyIdentity, agyIdentityMemo.found
	}
	agyIdentityMemo.checkedAt = time.Now()
	agyIdentityMemo.found = false
	path, err := findAgy()
	if err != nil {
		return agyIdentity{}, false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return agyIdentity{}, false
	}
	current := agyIdentityCache{Path: path, Size: info.Size(), ModNanos: info.ModTime().UnixNano()}
	if agyIdentityMemo.entry.Path == current.Path && agyIdentityMemo.entry.Size == current.Size && agyIdentityMemo.entry.ModNanos == current.ModNanos {
		agyIdentityMemo.found = true
		return agyIdentityMemo.entry.agyIdentity, true
	}
	if cached, ok := readAgyIdentityCache(current); ok {
		agyIdentityMemo.entry, agyIdentityMemo.found = cached, true
		return cached.agyIdentity, true
	}
	identity, ok := readAgyIdentity(path)
	if !ok {
		return agyIdentity{}, false
	}
	current.agyIdentity = identity
	writeAgyIdentityCache(current)
	agyIdentityMemo.entry, agyIdentityMemo.found = current, true
	return identity, true
}

func readAgyIdentityCache(want agyIdentityCache) (agyIdentityCache, bool) {
	file, err := config.HomePath("agy_identity.json")
	if err != nil {
		return agyIdentityCache{}, false
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return agyIdentityCache{}, false
	}
	var cached agyIdentityCache
	if json.Unmarshal(data, &cached) != nil {
		return agyIdentityCache{}, false
	}
	if cached.Path != want.Path || cached.Size != want.Size || cached.ModNanos != want.ModNanos ||
		!versionPattern.MatchString(cached.Version) || !changelistPattern.MatchString(cached.Changelist) {
		return agyIdentityCache{}, false
	}
	return cached, true
}

func writeAgyIdentityCache(entry agyIdentityCache) {
	file, err := config.HomePath("agy_identity.json")
	if err != nil {
		return
	}
	if _, err := config.EnsureHome(); err != nil {
		return
	}
	if data, err := json.Marshal(entry); err == nil {
		_ = os.WriteFile(file, data, 0o600)
	}
}

// readAgyIdentity asks the binary for its version and reads its build
// changelist out of the build metadata it carries (the google3 source path of
// the release it was cut from).
func readAgyIdentity(path string) (agyIdentity, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return agyIdentity{}, false
	}
	version := strings.TrimSpace(string(output))
	if fields := strings.Fields(version); len(fields) > 0 {
		version = fields[len(fields)-1]
	}
	if !versionPattern.MatchString(version) {
		return agyIdentity{}, false
	}
	changelist, ok := scanChangelist(path)
	if !ok {
		return agyIdentity{}, false
	}
	return agyIdentity{Version: version, Changelist: changelist}, true
}

// changelistMarker precedes the changelist in the build metadata:
// google_src/files/<changelist>/depot/google3.
var changelistMarker = []byte("google_src/files/")

// scanChangelist finds the first build-path marker in the binary. The file is
// read in blocks with an overlap so a marker that straddles two blocks is not
// missed, and the scan stops at the first hit.
func scanChangelist(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	const block = 4 << 20
	overlap := len(changelistMarker) + 24
	buffer := make([]byte, block+overlap)
	carried := 0
	for {
		read, err := io.ReadFull(file, buffer[carried:])
		window := buffer[:carried+read]
		for offset := 0; offset < len(window); {
			index := bytes.Index(window[offset:], changelistMarker)
			if index < 0 {
				break
			}
			rest := window[offset+index+len(changelistMarker):]
			if end := bytes.IndexByte(rest, '/'); end > 0 && end <= 12 && bytes.HasPrefix(rest[end:], []byte("/depot")) {
				if candidate := string(rest[:end]); changelistPattern.MatchString(candidate) {
					return candidate, true
				}
			}
			offset += index + len(changelistMarker)
		}
		if err != nil {
			return "", false
		}
		if len(window) > overlap {
			copy(buffer, window[len(window)-overlap:])
			carried = overlap
		} else {
			carried = len(window)
		}
	}
}

// agyBinaryName is the CLI's file name on this platform.
func agyBinaryName() string {
	if runtime.GOOS == "windows" {
		return "agy.exe"
	}
	return "agy"
}

// findAgy locates an installed Antigravity CLI: an explicit TERMIXGO_AGY_PATH,
// then PATH, then the places its installers put it (the standalone installer
// and the IDE's managed copy). The CLI is optional: it only supplies the
// release identity, and its absence leaves the built-in one.
func findAgy() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("TERMIXGO_AGY_PATH")); explicit != "" {
		if info, err := os.Stat(explicit); err == nil && !info.IsDir() {
			return explicit, nil
		}
		return "", fmt.Errorf("TERMIXGO_AGY_PATH points at %q, which is not a file", explicit)
	}
	if found, err := exec.LookPath("agy"); err == nil {
		return found, nil
	}
	var candidates []string
	if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
		candidates = append(candidates, filepath.Join(local, "agy", "bin", agyBinaryName()))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".gemini", "bin", agyBinaryName()),
			filepath.Join(home, ".local", "bin", agyBinaryName()),
			filepath.Join(home, ".agy", "bin", agyBinaryName()))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errAgyMissing
}

var errAgyMissing = errors.New("no Antigravity CLI (agy) was found")
