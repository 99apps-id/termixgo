package config

import (
	"os"
	"testing"
)

// TestEffortForPrefersTheModelOverride pins the resolution order: a per-model
// entry beats the session setting, and an unknown level is ignored rather than
// forwarded to a provider that would reject it.
func TestEffortForPrefersTheModelOverride(t *testing.T) {
	cfg := Default()
	cfg.Effort = "low"
	cfg.ModelEfforts = map[string]string{"planner": "high"}

	if got := cfg.EffortFor("planner"); got != "high" {
		t.Errorf("EffortFor(planner) = %q, want the per-model high", got)
	}
	if got := cfg.EffortFor("other"); got != "low" {
		t.Errorf("EffortFor(other) = %q, want the session-wide low", got)
	}

	cfg.ModelEfforts["planner"] = "ultra"
	if got := cfg.EffortFor("planner"); got != "low" {
		t.Errorf("an unknown model level should fall back to the session setting, got %q", got)
	}

	cfg.Effort = ""
	if got := cfg.EffortFor("planner"); got != "" {
		t.Errorf("EffortFor with nothing set = %q, want empty for the provider default", got)
	}
}

// TestEffortNormalisationDropsJunk covers the hand-edited file: a level no
// provider knows must not survive into a request.
func TestEffortNormalisationDropsJunk(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	raw := `{"version":1,"approvalMode":"all","maxSteps":10,"effort":"TURBO","modelEfforts":{"a":"HIGH","b":"nonsense"}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Effort != "" {
		t.Errorf("effort = %q, want the unknown level dropped", loaded.Effort)
	}
	if got := loaded.ModelEfforts["a"]; got != "high" {
		t.Errorf("modelEfforts[a] = %q, want high lowercased", got)
	}
	if _, present := loaded.ModelEfforts["b"]; present {
		t.Errorf("modelEfforts[b] should be dropped, got %q", loaded.ModelEfforts["b"])
	}
}
