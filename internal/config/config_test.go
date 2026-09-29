package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestApprovalModeRoundTrip(t *testing.T) {
	cases := []struct {
		input string
		want  ApprovalMode
		ok    bool
	}{
		{"ask", ApprovalAsk, true},
		{"EDITS", ApprovalEdits, true},
		{" all ", ApprovalAll, true},
		{"as", ApprovalAsk, true},
		{"al", ApprovalAll, true},
		{"ed", ApprovalEdits, true},
		// "a" matches both ask and all, so it must stay ambiguous.
		{"a", "", false},
		{"nonsense", "", false},
		{"", "", false},
	}
	for _, testCase := range cases {
		got, err := ParseApprovalMode(testCase.input)
		if testCase.ok && err != nil {
			t.Errorf("ParseApprovalMode(%q) unexpected error: %v", testCase.input, err)
			continue
		}
		if !testCase.ok {
			if err == nil {
				t.Errorf("ParseApprovalMode(%q) expected an error", testCase.input)
			}
			continue
		}
		if got != testCase.want {
			t.Errorf("ParseApprovalMode(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}

func TestTrustCoversChildFolders(t *testing.T) {
	root := t.TempDir()
	cfg := Default().Trust(root)

	if !cfg.IsTrusted(root) {
		t.Fatalf("the trusted root should be trusted")
	}
	child := filepath.Join(root, "internal", "agent")
	if !cfg.IsTrusted(child) {
		t.Fatalf("a folder under a trusted root should be trusted, got untrusted for %s", child)
	}
	if cfg.IsTrusted(t.TempDir()) {
		t.Fatalf("an unrelated folder must not be trusted")
	}

	untrusted := cfg.Untrust(root)
	if untrusted.IsTrusted(root) {
		t.Fatalf("Untrust should remove the folder")
	}
}

func TestTrustIsCaseInsensitiveOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case folding only applies on Windows")
	}
	dir := t.TempDir()
	cfg := Default().Trust(dir)
	if !cfg.IsTrusted(uppercase(dir)) {
		t.Fatalf("trust should fold case on Windows")
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())

	cfg := Default()
	cfg.DefaultModel = "claude-sonnet-4-5"
	cfg.ApprovalMode = ApprovalEdits
	cfg.AlwaysAllowedTools = []string{"run_command"}
	cfg.Telegram.Enabled = true
	cfg.Telegram.ChatID = 42

	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.DefaultModel != cfg.DefaultModel {
		t.Errorf("DefaultModel = %q, want %q", loaded.DefaultModel, cfg.DefaultModel)
	}
	if loaded.ApprovalMode != ApprovalEdits {
		t.Errorf("ApprovalMode = %q, want edits", loaded.ApprovalMode)
	}
	if len(loaded.AlwaysAllowedTools) != 1 || loaded.AlwaysAllowedTools[0] != "run_command" {
		t.Errorf("AlwaysAllowedTools = %v, want [run_command]", loaded.AlwaysAllowedTools)
	}
	if !loaded.Telegram.Enabled || loaded.Telegram.ChatID != 42 {
		t.Errorf("Telegram = %+v, want enabled with chat 42", loaded.Telegram)
	}
}

func TestLoadRepairsInvalidValues(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"approvalMode":"bogus","maxSteps":0,"language":""}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ApprovalMode != ApprovalAll {
		t.Errorf("ApprovalMode = %q, want the default all", cfg.ApprovalMode)
	}
	if cfg.MaxSteps != 100 {
		t.Errorf("MaxSteps = %d, want 100", cfg.MaxSteps)
	}
	if cfg.Language != "en" {
		t.Errorf("Language = %q, want en", cfg.Language)
	}
}

func TestWithRecentDeduplicates(t *testing.T) {
	cfg := Default().WithRecent("/a").WithRecent("/b").WithRecent("/a")
	if len(cfg.RecentProjects) != 2 {
		t.Fatalf("RecentProjects = %v, want two entries", cfg.RecentProjects)
	}
	if cfg.RecentProjects[0] != "/a" {
		t.Errorf("the newest project should be first, got %v", cfg.RecentProjects)
	}
}

func uppercase(value string) string {
	result := []rune(value)
	for index, r := range result {
		if r >= 'a' && r <= 'z' {
			result[index] = r - 32
		}
	}
	return string(result)
}
