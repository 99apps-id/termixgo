package provider

import "testing"

// TestEffortMappingPerProvider pins each vendor's own spelling of the shared
// level. These fields are strict, so the mapping is the whole contract: a value
// the endpoint does not know comes back as a 400 and loses the turn.
func TestEffortMappingPerProvider(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		effort   string
		want     string
	}{
		{"openai low", "openai", EffortLow, "low"},
		{"openai medium", "openai", EffortMedium, "medium"},
		{"openai high", "openai", EffortHigh, "high"},
		// OpenAI has no level above high, so the deepest request becomes the
		// highest value it does accept rather than an invalid field.
		{"openai max folds to high", "openai", EffortMax, "high"},
		{"deepseek max", "deepseek", EffortMax, "high"},
		{"xai high", "xai", EffortHigh, "high"},
		{"kimi has no medium", "moonshot", EffortMedium, "high"},
		{"kimi max", "moonshot", EffortMax, "max"},
		{"glm low", "zhipu", EffortLow, "low"},
		{"glm medium becomes high", "zhipu", EffortMedium, "high"},
		{"glm max", "zhipu", EffortMax, "max"},
		// A provider with no reasoning control is sent nothing at all, which is
		// the safe answer for an endpoint whose fields are unknown.
		{"openrouter unknown", "openrouter", EffortHigh, ""},
		{"groq unknown", "groq", EffortHigh, ""},
		{"custom unknown", "custom", EffortHigh, ""},
		{"empty effort", "openai", "", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := compatibleEffort(testCase.provider, testCase.effort); got != testCase.want {
				t.Errorf("compatibleEffort(%q, %q) = %q, want %q", testCase.provider, testCase.effort, got, testCase.want)
			}
		})
	}
}

// TestGoogleEffortMapsToLevelAndBudget covers the Gemini split: Gemini 3 takes
// a named level and the 2.5 series takes a token budget instead. Writing both
// would be rejected.
func TestGoogleEffortMapsToLevelAndBudget(t *testing.T) {
	if got := googleEffort(EffortLow); got != "low" {
		t.Errorf("googleEffort(low) = %q, want low", got)
	}
	if got := googleEffort(EffortMax); got != "high" {
		t.Errorf("googleEffort(max) = %q, want high because Gemini 3 has no level above it", got)
	}
	if got := googleEffort(""); got != "" {
		t.Errorf("googleEffort(empty) = %q, want empty", got)
	}
	for _, testCase := range []struct {
		effort string
		want   int
	}{
		{EffortLow, 1024},
		{EffortMedium, 8192},
		{EffortHigh, 24576},
		{EffortMax, 32768},
		{"", 0},
	} {
		if got := googleThinkingBudget(testCase.effort); got != testCase.want {
			t.Errorf("googleThinkingBudget(%q) = %d, want %d", testCase.effort, got, testCase.want)
		}
	}
}

// TestAnthropicEffortPassesEveryLevel keeps max from being folded away: unlike
// OpenAI, the Anthropic effort parameter takes all four levels.
func TestAnthropicEffortPassesEveryLevel(t *testing.T) {
	for _, level := range []string{EffortLow, EffortMedium, EffortHigh, EffortMax} {
		if got := anthropicEffortValues(level); got != level {
			t.Errorf("anthropicEffortValues(%q) = %q, want the same level", level, got)
		}
	}
	if got := anthropicEffortValues(""); got != "" {
		t.Errorf("anthropicEffortValues(empty) = %q, want empty", got)
	}
}
