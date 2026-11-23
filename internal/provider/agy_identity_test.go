package provider

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func stubAgyIdentity(t *testing.T, identity agyIdentity, found bool) {
	t.Helper()
	previous := agyIdentityLookup
	agyIdentityLookup = func() (agyIdentity, bool) { return identity, found }
	t.Cleanup(func() { agyIdentityLookup = previous })
}

func clearAgyIdentityEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"TERMIXGO_ANTIGRAVITY_CLI_VERSION", "ANTIGRAVITY_CLI_VERSION", "ODON_ANTIGRAVITY_CLI_VERSION", "TERMIXGO_ANTIGRAVITY_CLI_CL"} {
		t.Setenv(key, "")
	}
}

// TestAntigravityIdentityFollowsTheInstalledCLI is the update rule: the
// version and changelist come from the CLI installed on the machine, so an agy
// that updated itself changes the User-Agent with no release of Termixgo.
func TestAntigravityIdentityFollowsTheInstalledCLI(t *testing.T) {
	clearAgyIdentityEnv(t)
	stubAgyIdentity(t, agyIdentity{Version: "1.9.3", Changelist: "1099887766"}, true)
	if agCLIVersion() != "1.9.3" || agChangelist() != "1099887766" {
		t.Fatalf("version=%q changelist=%q, want the installed CLI's", agCLIVersion(), agChangelist())
	}
	if ua := agUserAgent(); !bytes.Contains([]byte(ua), []byte("antigravity/cli/1.9.3 (")) || !bytes.Contains([]byte(ua), []byte("cl=1099887766;")) {
		t.Errorf("User-Agent = %q", ua)
	}
}

func TestAntigravityIdentityFallsBackToTheBuiltInPair(t *testing.T) {
	clearAgyIdentityEnv(t)
	stubAgyIdentity(t, agyIdentity{}, false)
	if agCLIVersion() != defaultAGCLIVersion || agChangelist() != defaultAGChangelist {
		t.Fatalf("version=%q changelist=%q, want the built-in pair", agCLIVersion(), agChangelist())
	}
}

// A pin from the environment wins over the installed CLI, and a pinned version
// drops the installed changelist: the pair is only valid together.
func TestAntigravityIdentityPinWinsOverTheInstalledCLI(t *testing.T) {
	clearAgyIdentityEnv(t)
	stubAgyIdentity(t, agyIdentity{Version: "1.9.3", Changelist: "1099887766"}, true)
	t.Setenv("TERMIXGO_ANTIGRAVITY_CLI_VERSION", "1.6.0")
	if agCLIVersion() != "1.6.0" || agChangelist() != "" {
		t.Fatalf("version=%q changelist=%q, want the pin and no changelist", agCLIVersion(), agChangelist())
	}
	t.Setenv("TERMIXGO_ANTIGRAVITY_CLI_CL", "1000000001")
	if agChangelist() != "1000000001" {
		t.Fatalf("changelist = %q, want the pinned one", agChangelist())
	}
}

func writeFakeBinary(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agy.bin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScanChangelistFindsTheBuildPath(t *testing.T) {
	filler := bytes.Repeat([]byte{0x90}, 1024)
	content := append(append(append([]byte{}, filler...), []byte("google_src/files/1027820220/depot/google3")...), filler...)
	if got, ok := scanChangelist(writeFakeBinary(t, content)); !ok || got != "1027820220" {
		t.Fatalf("scanChangelist = %q, %v", got, ok)
	}
}

// A marker that is not followed by a changelist and the depot path is skipped
// in favour of a real one later in the same window.
func TestScanChangelistSkipsAMarkerThatIsNotABuildPath(t *testing.T) {
	content := []byte("xx google_src/files/notanumber/depot yy google_src/files/12/depot zz google_src/files/1034455667/depot/google3")
	if got, ok := scanChangelist(writeFakeBinary(t, content)); !ok || got != "1034455667" {
		t.Fatalf("scanChangelist = %q, %v", got, ok)
	}
}

// The marker can straddle a block boundary of the read loop.
func TestScanChangelistFindsAMarkerAcrossABlockBoundary(t *testing.T) {
	marker := []byte("google_src/files/1055667788/depot/google3")
	// The first window holds block+overlap bytes; the marker starts ten bytes
	// before its end, so it is split between two reads.
	window := (4 << 20) + len(changelistMarker) + 24
	content := append(bytes.Repeat([]byte{0x41}, window-10), marker...)
	if got, ok := scanChangelist(writeFakeBinary(t, content)); !ok || got != "1055667788" {
		t.Fatalf("scanChangelist = %q, %v", got, ok)
	}
}

func TestScanChangelistReportsAMissingMarker(t *testing.T) {
	if got, ok := scanChangelist(writeFakeBinary(t, []byte("no build metadata here"))); ok {
		t.Fatalf("scanChangelist = %q, want no result", got)
	}
}

func TestAgyIdentityCacheMatchesOnlyTheSameBinary(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	entry := agyIdentityCache{Path: "/x/agy", Size: 100, ModNanos: 7, agyIdentity: agyIdentity{Version: "1.5.15", Changelist: "1027820220"}}
	writeAgyIdentityCache(entry)
	if got, ok := readAgyIdentityCache(entry); !ok || got.Version != "1.5.15" || got.Changelist != "1027820220" {
		t.Fatalf("cache = %+v, %v", got, ok)
	}
	for _, changed := range []agyIdentityCache{
		{Path: "/x/agy", Size: 101, ModNanos: 7},
		{Path: "/x/agy", Size: 100, ModNanos: 8},
		{Path: "/y/agy", Size: 100, ModNanos: 7},
	} {
		if _, ok := readAgyIdentityCache(changed); ok {
			t.Errorf("a different binary %+v must not hit the cache", changed)
		}
	}
}
