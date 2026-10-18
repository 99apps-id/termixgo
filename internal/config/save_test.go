package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveReportsAnUnwritableTemporaryFile covers the write step. A settings
// file that silently fails to save is the worst outcome: the operator changes a
// preference, sees no error, and finds it reverted after a restart.
func TestSaveReportsAnUnwritableTemporaryFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	// A directory where the temporary file belongs makes the write fail after
	// the state directory itself was prepared.
	if err := os.MkdirAll(filepath.Join(home, FileName+".tmp"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	err := Save(Default())
	if err == nil {
		t.Fatalf("a write that cannot land must be reported")
	}
	if !strings.Contains(err.Error(), "write config") {
		t.Errorf("err = %v, want it to name the step that failed", err)
	}
	// Nothing may be left behind for the next load to trip over.
	if _, statErr := os.Stat(filepath.Join(home, FileName)); statErr == nil {
		t.Errorf("no settings file should have been created")
	}
}

// TestSaveReportsAFailedRename covers the replace step. A directory in place of
// the settings file is what a botched manual edit leaves behind, and the error
// has to name the replace rather than the write.
func TestSaveReportsAFailedRename(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	if err := os.MkdirAll(filepath.Join(home, FileName), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	err := Save(Default())
	if err == nil {
		t.Fatalf("replacing a directory with a file must be reported")
	}
	if !strings.Contains(err.Error(), "replace config") {
		t.Errorf("err = %v, want it to name the replace step", err)
	}
	// The temporary file is cleaned up, so a later save is not blocked by it.
	if _, statErr := os.Stat(filepath.Join(home, FileName+".tmp")); statErr == nil {
		t.Errorf("the temporary file was left behind")
	}
}

// TestSaveStampsTheVersion keeps a hand-edited or absent version from reaching
// disk, which is what a future migration reads.
func TestSaveStampsTheVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)

	cfg := Default()
	cfg.Version = 0
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Version != 1 {
		t.Errorf("version = %d, want 1", loaded.Version)
	}
	// The file ends with a newline, so a text tool sees a well-formed file.
	raw, err := os.ReadFile(filepath.Join(home, FileName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Errorf("the settings file should end with a newline")
	}
}

// TestSaveLeavesNoTemporaryFile pins the atomicity claim: the rename is the
// only visible step.
func TestSaveLeavesNoTemporaryFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	for index := 0; index < 3; index++ {
		if err := Save(Default()); err != nil {
			t.Fatalf("Save %d: %v", index, err)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("a temporary file survived: %s", entry.Name())
		}
	}
}

// TestPathFailsWhenHomeCannotBeResolved keeps the error text actionable rather
// than a bare empty string.
func TestPathFailsWhenHomeCannotBeResolved(t *testing.T) {
	t.Setenv(EnvHome, "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	home, err := Home()
	if err == nil {
		// A platform that still resolves a home directory is fine; the point of
		// the test is that the pair either works or reports why.
		if !filepath.IsAbs(home) {
			t.Errorf("home = %q, want an absolute path", home)
		}
		return
	}
	if !strings.Contains(err.Error(), "home") {
		t.Errorf("err = %v, want it to name the home directory", err)
	}
	if _, err := Path(); err == nil {
		t.Errorf("Path must report the same failure")
	}
}
