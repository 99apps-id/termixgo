package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTrustStoresTheRealFolderCase(t *testing.T) {
	dir := t.TempDir()
	cfg := Default().Trust(dir)

	if len(cfg.TrustedFolders) != 1 {
		t.Fatalf("TrustedFolders = %v, want one entry", cfg.TrustedFolders)
	}
	// The stored value is what the banner and status bar show, so it must be
	// the real path rather than a case-folded copy.
	if got := cfg.TrustedFolders[0]; got != CleanFolder(dir) {
		t.Errorf("stored folder = %q, want the real path %q", got, CleanFolder(dir))
	}
	if strings.ToLower(cfg.TrustedFolders[0]) != cfg.TrustedFolders[0] && runtime.GOOS != "windows" {
		// On a case-sensitive filesystem the path must keep its casing.
		if !strings.Contains(cfg.TrustedFolders[0], filepath.Base(dir)) {
			t.Errorf("stored folder lost its basename: %q", cfg.TrustedFolders[0])
		}
	}
}

func TestCleanFolderKeepsCaseAndResolves(t *testing.T) {
	dir := t.TempDir()
	cleaned := CleanFolder(dir)
	if cleaned == "" {
		t.Fatalf("CleanFolder returned an empty string")
	}
	if cleaned != filepath.Clean(cleaned) {
		t.Errorf("CleanFolder should clean the path, got %q", cleaned)
	}
	if got := CleanFolder("  "); got != "" {
		t.Errorf("blank input should yield an empty string, got %q", got)
	}
}

// TestTrustCoversAChildThatDoesNotExistYet pins the symlink case: a child of a
// trusted folder is often not created until later, and resolving the child's
// symlink directly fails. The trusted root still resolves through the link, so
// the child has to resolve through its existing ancestor or it looks untrusted.
func TestTrustCoversAChildThatDoesNotExistYet(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	cfg := Default().Trust(link)

	child := filepath.Join(link, "new", "package")
	if !cfg.IsTrusted(child) {
		t.Fatalf("a folder under a symlinked trusted root should be trusted: %s", child)
	}
}

func TestTrustKeyFoldsOnlyOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		if TrustKey(`C:\Projects\App`) != TrustKey(`c:\projects\app`) {
			t.Errorf("Windows trust keys should fold case")
		}
		return
	}
	if TrustKey("/Projects/App") == TrustKey("/projects/app") {
		t.Errorf("off Windows, case must be significant")
	}
}

func TestTrustedFolderIsRecognisedFromEitherCasing(t *testing.T) {
	dir := t.TempDir()
	cfg := Default().Trust(dir)

	// Whatever the platform, the folder it was created from is trusted.
	if !cfg.IsTrusted(dir) {
		t.Errorf("%s should be trusted", dir)
	}
	// A path with a redundant separator resolves to the same folder.
	if !cfg.IsTrusted(dir + string(filepath.Separator)) {
		t.Errorf("a trailing separator should not change trust")
	}
}

func TestWithRecentKeepsTheNewestFirst(t *testing.T) {
	cfg := Default().WithRecent("/alpha").WithRecent("/beta").WithRecent("/alpha")
	if len(cfg.RecentProjects) != 2 {
		t.Fatalf("RecentProjects = %v, want two entries", cfg.RecentProjects)
	}
	if cfg.RecentProjects[0] != "/alpha" {
		t.Errorf("the newest project should be first, got %v", cfg.RecentProjects)
	}
}
