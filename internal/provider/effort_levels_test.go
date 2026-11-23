package provider

import "testing"

func TestClampEffortLowersToWhatTheModelOffers(t *testing.T) {
	cases := []struct {
		effort    string
		supported []string
		want      string
	}{
		{EffortUltra, levelsUpTo(EffortMax), EffortMax},
		{EffortXHigh, levelsUpTo(EffortHigh), EffortHigh},
		{EffortMedium, []string{EffortLow, EffortHigh, EffortMax}, EffortLow},
		{EffortLow, []string{EffortMedium, EffortHigh}, EffortMedium},
		{"", levelsUpTo(EffortMax), ""},
		{"bogus", levelsUpTo(EffortMax), ""},
	}
	for _, tc := range cases {
		if got := clampEffort(tc.effort, tc.supported); got != tc.want {
			t.Errorf("clampEffort(%q, %v) = %q, want %q", tc.effort, tc.supported, got, tc.want)
		}
	}
}

// TestCodexEffortFollowsTheModelCatalogue pins the levels the ChatGPT backend
// itself lists per model: ultra only where it is listed, max where it is, and
// the conservative set for a model it does not name.
func TestCodexEffortFollowsTheModelCatalogue(t *testing.T) {
	cases := []struct{ model, effort, want string }{
		{"gpt-6.2-sol", EffortUltra, "ultra"},
		{"gpt-6.2-astra", EffortXHigh, "xhigh"},
		{"gpt-6.1-terra", EffortMax, "max"},
		{"gpt-5.6-sol", EffortUltra, "ultra"},
		{"gpt-5.6-terra", EffortUltra, "ultra"},
		{"gpt-5.6-luna", EffortUltra, "max"},
		{"gpt-6-luna", EffortUltra, "max"},
		{"gpt-reserve", EffortUltra, "max"},
		{"gpt-5.5", EffortUltra, "xhigh"},
		{"gpt-5.3-codex", EffortMax, "xhigh"},
		{"gpt-5-codex", EffortXHigh, "high"},
		{"gpt-5.6-sol", "", ""},
	}
	for _, tc := range cases {
		if got := codexEffort(tc.model, tc.effort); got != tc.want {
			t.Errorf("codexEffort(%q, %q) = %q, want %q", tc.model, tc.effort, got, tc.want)
		}
	}
}

func TestAnthropicEffortFollowsTheModel(t *testing.T) {
	cases := []struct{ model, effort, want string }{
		{"claude-fable-5-2", EffortXHigh, "xhigh"},
		{"claude-opus-5-5", EffortUltra, "max"},
		{"claude-sonnet-5.5", EffortXHigh, "xhigh"},
		{"claude-haiku-5-5", EffortMax, "max"},
		{"claude-opus-4-7", EffortXHigh, "xhigh"},
		{"claude-opus-4-6", EffortXHigh, "high"},
		{"claude-opus-4-6", EffortMax, "max"},
		{"claude-sonnet-4-5", EffortMax, "high"},
		{"claude-haiku-4-5", EffortXHigh, "high"},
		{"claude-opus-5-5", "", ""},
	}
	for _, tc := range cases {
		if got := anthropicEffort(tc.model, tc.effort); got != tc.want {
			t.Errorf("anthropicEffort(%q, %q) = %q, want %q", tc.model, tc.effort, got, tc.want)
		}
	}
}

func TestCopilotEffortFollowsTheLiveCatalogue(t *testing.T) {
	cases := []struct{ model, effort, want string }{
		{"gpt-6.1-sol", EffortMax, "max"},
		{"gpt-5.6-luna", EffortUltra, "max"},
		{"gpt-5.3-codex", EffortMax, "xhigh"},
		{"grok-4.7", EffortMax, "xhigh"},
		{"gemini-4-hydrogen", EffortUltra, "xhigh"},
		{"gemini-4-argon", EffortXHigh, "high"},
		{"kimi-k3", EffortMedium, "low"},
		{"kimi-k3", EffortUltra, "max"},
		{"mai-code-2", EffortUltra, "max"},
		{"mai-code-1.1-flash", EffortMax, "high"},
	}
	for _, tc := range cases {
		if got := compatibleEffortFor("github-copilot", tc.model, tc.effort); got != tc.want {
			t.Errorf("copilot effort(%q, %q) = %q, want %q", tc.model, tc.effort, got, tc.want)
		}
	}
}

func TestEveryLevelReachesAProviderThatCannotTakeIt(t *testing.T) {
	// The providers that know only low, medium and high must never see xhigh,
	// max or ultra: those fields are strict and an unknown value is a 400.
	for _, level := range []string{EffortXHigh, EffortMax, EffortUltra} {
		if got := openAIEffortValues(level); got != "high" {
			t.Errorf("openAIEffortValues(%q) = %q, want high", level, got)
		}
		if got := googleEffort(level); got != "high" {
			t.Errorf("googleEffort(%q) = %q, want high", level, got)
		}
	}
	if got := museEffort(EffortUltra); got != "max" {
		t.Errorf("museEffort(ultra) = %q, want max", got)
	}
	if got := museEffort(EffortXHigh); got != "xhigh" {
		t.Errorf("museEffort(xhigh) = %q, want xhigh", got)
	}
}
