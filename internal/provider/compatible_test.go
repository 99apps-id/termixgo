package provider

import "testing"

func TestIsCompatible(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{CompatibleID, true},
		{"openai-compatible/foo", true},
		{"openai-compatible/local-ollama", true},
		{"openai-compatible/", false},
		{"openai", false},
		{"openai-compatiblex", false},
		{"xopenai-compatible/foo", false},
		{"", false},
	}
	for _, tc := range tests {
		got := IsCompatible(tc.id)
		if got != tc.want {
			t.Errorf("IsCompatible(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestCompatibleName(t *testing.T) {
	if got := CompatibleName(CompatibleID); got != "" {
		t.Errorf("CompatibleName(%q) = %q, want empty", CompatibleID, got)
	}
	if got := CompatibleName("openai-compatible/foo"); got != "foo" {
		t.Errorf("CompatibleName(%q) = %q, want foo", "openai-compatible/foo", got)
	}
	if got := CompatibleName("openai"); got != "" {
		t.Errorf("CompatibleName(%q) = %q, want empty", "openai", got)
	}
}

func TestCompatibleProviderID(t *testing.T) {
	if got := CompatibleProviderID(""); got != CompatibleID {
		t.Errorf("CompatibleProviderID(%q) = %q, want %q", "", got, CompatibleID)
	}
	if got := CompatibleProviderID("foo"); got != "openai-compatible/foo" {
		t.Errorf("CompatibleProviderID(%q) = %q, want openai-compatible/foo", "foo", got)
	}
	if got := CompatibleProviderID("  bar  "); got != "openai-compatible/bar" {
		t.Errorf("CompatibleProviderID(%q) = %q, want openai-compatible/bar", "  bar  ", got)
	}
}

func TestCompatibleLabel(t *testing.T) {
	if got := CompatibleLabel(""); got != "OpenAI Compatible" {
		t.Errorf("CompatibleLabel(%q) = %q, want OpenAI Compatible", "", got)
	}
	if got := CompatibleLabel("foo"); got != "OpenAI Compatible (foo)" {
		t.Errorf("CompatibleLabel(%q) = %q, want OpenAI Compatible (foo)", "foo", got)
	}
}

func TestByIDSynthesizesNamedCompatible(t *testing.T) {
	static, _ := ByID(CompatibleID)
	info, ok := ByID("openai-compatible/local-ollama")
	if !ok {
		t.Fatalf("ByID should synthesize a named compatible provider")
	}
	if info.ID != "openai-compatible/local-ollama" {
		t.Errorf("ID = %q, want openai-compatible/local-ollama", info.ID)
	}
	if info.Kind != KindOpenAI {
		t.Errorf("Kind = %q, want openai", info.Kind)
	}
	if info.Label != CompatibleLabel("local-ollama") {
		t.Errorf("Label = %q, want %q", info.Label, CompatibleLabel("local-ollama"))
	}
	if info.DefaultBaseURL != "" {
		t.Errorf("DefaultBaseURL should be empty for named instances, got %q", info.DefaultBaseURL)
	}
	if info.NeedsKey != static.NeedsKey {
		t.Errorf("NeedsKey = %v, want %v", info.NeedsKey, static.NeedsKey)
	}
}

func TestByIDRejectsBogusCompatibleID(t *testing.T) {
	if _, ok := ByID("openai-compatible/"); ok {
		t.Errorf("ByID should reject an empty-name compatible id")
	}
}
