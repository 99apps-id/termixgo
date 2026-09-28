package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHomeHonoursTheOverride(t *testing.T) {
	override := t.TempDir()
	t.Setenv(EnvHome, override)
	home, err := Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	if home != override {
		t.Errorf("home = %q, want the override %q", home, override)
	}
	// A blank override must not win, or an exported-but-empty variable would
	// point the state directory at the process working directory.
	t.Setenv(EnvHome, "   ")
	home, err = Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	if home == "" || !strings.HasSuffix(home, ".termixgo") {
		t.Errorf("home = %q, want the default state directory", home)
	}
}

func TestHomeFallsBackToTheUserHome(t *testing.T) {
	t.Setenv(EnvHome, "")
	fake := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", fake)
	} else {
		t.Setenv("HOME", fake)
	}
	home, err := Home()
	if err != nil {
		t.Fatalf("Home: %v", err)
	}
	if home != filepath.Join(fake, ".termixgo") {
		t.Errorf("home = %q, want it under %q", home, fake)
	}
}

func TestEnsureHomeCreatesTheDirectory(t *testing.T) {
	t.Setenv(EnvHome, filepath.Join(t.TempDir(), "state"))
	home, err := EnsureHome()
	if err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("%s should be a directory", home)
	}
	// Idempotent: the wizard and the agent both call it.
	if _, err := EnsureHome(); err != nil {
		t.Errorf("a second call must succeed: %v", err)
	}
}

func TestHomePathIsInsideTheStateDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	path, err := HomePath("secrets.json")
	if err != nil {
		t.Fatalf("HomePath: %v", err)
	}
	if path != filepath.Join(home, "secrets.json") {
		t.Errorf("path = %q", path)
	}
}

func TestSessionsDirIsCreatedUnderTheStateDirectory(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state")
	t.Setenv(EnvHome, home)
	dir, err := SessionsDir()
	if err != nil {
		t.Fatalf("SessionsDir: %v", err)
	}
	if dir != filepath.Join(home, "sessions") {
		t.Errorf("dir = %q", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("%s should be a directory", dir)
	}
}

func TestEnsureHomeReportsAnUnusablePath(t *testing.T) {
	// A file where the state directory should be is the realistic failure: a
	// leftover with the same name. It must be reported, not ignored.
	blocker := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv(EnvHome, blocker)
	if _, err := EnsureHome(); err == nil {
		t.Fatalf("a file in the way must be an error")
	}
	if _, err := SessionsDir(); err == nil {
		t.Fatalf("SessionsDir should report the same problem")
	}
}

func TestSaveReportsAnUnusablePath(t *testing.T) {
	// The state directory's parent is a file, so the directory cannot be made.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv(EnvHome, filepath.Join(blocker, "state"))
	if err := Save(Default()); err == nil {
		t.Fatalf("saving under a file must fail")
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	if err := os.WriteFile(filepath.Join(home, FileName), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load()
	if err == nil {
		t.Fatalf("malformed settings must be reported, not silently ignored")
	}
	if cfg.DefaultModel != "" || cfg.MaxSteps == 0 {
		t.Errorf("a failed load should still hand back usable defaults, got %+v", cfg)
	}
}

func TestLoadReturnsDefaultsWhenTheFileIsAbsent(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxSteps != 25 || cfg.ApprovalMode != ApprovalAll {
		t.Errorf("cfg = %+v, want defaults", cfg)
	}
}

func TestNormaliseRepairsAHandEditedFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	// Every field here is one a person can get wrong by hand.
	broken := `{
	  "approvalMode": "sometimes",
	  "maxSteps": -4,
	  "language": "",
	  "costBudgetUsd": -12,
	  "baseUrls": null
	}`
	if err := os.WriteFile(filepath.Join(home, FileName), []byte(broken), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ApprovalMode != ApprovalAll {
		t.Errorf("approvalMode = %q, want it repaired to all", cfg.ApprovalMode)
	}
	if cfg.MaxSteps != 25 {
		t.Errorf("maxSteps = %d, want 25", cfg.MaxSteps)
	}
	if cfg.Language != "en" {
		t.Errorf("language = %q, want en", cfg.Language)
	}
	if cfg.CostBudgetUSD != 0 {
		t.Errorf("costBudgetUsd = %v, want 0", cfg.CostBudgetUSD)
	}
	if cfg.BaseURLs == nil {
		t.Errorf("a nil baseUrls map must be replaced, or lookups panic")
	}
}

func TestBaseURLPrefersTheConfiguredEndpoint(t *testing.T) {
	cfg := Default()
	if got := cfg.BaseURL("ollama", "http://localhost:11434/v1"); got != "http://localhost:11434/v1" {
		t.Errorf("BaseURL = %q, want the fallback", got)
	}
	cfg.BaseURLs = map[string]string{"ollama": "http://box:11434/v1"}
	if got := cfg.BaseURL("ollama", "http://localhost:11434/v1"); got != "http://box:11434/v1" {
		t.Errorf("BaseURL = %q, want the configured endpoint", got)
	}
	// A blank entry is not a configuration, so the fallback still applies.
	cfg.BaseURLs = map[string]string{"ollama": "  "}
	if got := cfg.BaseURL("ollama", "http://localhost:11434/v1"); got != "http://localhost:11434/v1" {
		t.Errorf("BaseURL = %q, want the fallback for a blank entry", got)
	}
}

func TestFolderExistsChecksForADirectory(t *testing.T) {
	dir := t.TempDir()
	if !FolderExists(dir) {
		t.Errorf("%s exists and is a directory", dir)
	}
	if FolderExists(filepath.Join(dir, "missing")) {
		t.Errorf("a missing path must report false")
	}
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if FolderExists(file) {
		t.Errorf("a file is not a folder")
	}
}

func TestTrustIsIdempotent(t *testing.T) {
	cfg := Default()
	folder := t.TempDir()
	once := cfg.Trust(folder)
	twice := once.Trust(folder)
	if len(once.TrustedFolders) != 1 || len(twice.TrustedFolders) != 1 {
		t.Errorf("trusting twice must not duplicate the folder: %v then %v", once.TrustedFolders, twice.TrustedFolders)
	}
	// Trust must return a copy, never mutate the receiver's slice in place:
	// the caller stores the result and the original is not the live config.
	if len(cfg.TrustedFolders) != 0 {
		t.Errorf("Trust mutated the receiver: %v", cfg.TrustedFolders)
	}
	if trusted := cfg.Trust("   "); len(trusted.TrustedFolders) != 0 {
		t.Errorf("a blank folder must not be trusted: %v", trusted.TrustedFolders)
	}
}

func TestTrustKeyOfABlankPathIsEmpty(t *testing.T) {
	if got := TrustKey("  "); got != "" {
		t.Errorf("TrustKey = %q, want empty", got)
	}
	if got := TrustKey(""); got != "" {
		t.Errorf("TrustKey = %q, want empty", got)
	}
}

func TestUntrustRemovesEveryCasing(t *testing.T) {
	dir := t.TempDir()
	cfg := Default().Trust(dir)
	if len(cfg.TrustedFolders) != 1 {
		t.Fatalf("setup: %v", cfg.TrustedFolders)
	}
	removed := cfg.Untrust(dir)
	if len(removed.TrustedFolders) != 0 {
		t.Errorf("Untrust left %v", removed.TrustedFolders)
	}
	if removed.IsTrusted(dir) {
		t.Errorf("the folder is still trusted after Untrust")
	}

	// On Windows the same folder spelled with different casing is the same
	// folder, so it must still be removable. Elsewhere the spellings really
	// are different paths and only the exact form can match.
	if runtime.GOOS == "windows" {
		again := Default().Trust(dir)
		if len(again.Untrust(strings.ToUpper(dir)).TrustedFolders) != 0 {
			t.Errorf("case folding should remove the entry on Windows")
		}
	}
}

func TestIsTrustedIgnoresUnusableEntries(t *testing.T) {
	cfg := Default()
	cfg.TrustedFolders = []string{"", "   "}
	if cfg.IsTrusted("/tmp/anything") {
		t.Errorf("empty trust entries must be skipped")
	}
	if cfg.IsTrusted("  ") {
		t.Errorf("a blank folder is never trusted")
	}
}

func TestWithRecentCapsTheList(t *testing.T) {
	cfg := Default()
	for index := 0; index < 15; index++ {
		cfg = cfg.WithRecent(filepath.Join(t.TempDir(), "project", string(rune('a'+index))))
	}
	if len(cfg.RecentProjects) > 10 {
		t.Errorf("recent projects grew to %d, want at most 10", len(cfg.RecentProjects))
	}
	if got := cfg.WithRecent("  "); len(got.RecentProjects) != len(cfg.RecentProjects) {
		t.Errorf("a blank path must not enter the list")
	}
}
