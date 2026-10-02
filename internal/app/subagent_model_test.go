package app

import (
	"errors"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestResolveSubagentModelForms(t *testing.T) {
	cases := []struct {
		query        string
		wantProvider string
		wantWire     string
	}{
		// A catalogue id.
		{"antigravity-gemini-3.8-flash", "antigravity", "gemini-3.8-flash-medium"},
		// provider:model, the form ModelFromQuery understands.
		{"github-copilot:claude-sonnet-5.5", "github-copilot", "claude-sonnet-5.5"},
		// provider/model, convenient for a vendor wire id.
		{"github-copilot/claude-sonnet-5.5", "github-copilot", "claude-sonnet-5.5"},
	}
	for _, testCase := range cases {
		model, err := resolveSubagentModel(config.Config{}, testCase.query)
		if err != nil {
			t.Errorf("resolveSubagentModel(%q): %v", testCase.query, err)
			continue
		}
		if model.Provider != testCase.wantProvider {
			t.Errorf("resolveSubagentModel(%q) provider = %q, want %q", testCase.query, model.Provider, testCase.wantProvider)
		}
		if model.WireID() != testCase.wantWire {
			t.Errorf("resolveSubagentModel(%q) wire = %q, want %q", testCase.query, model.WireID(), testCase.wantWire)
		}
	}

	for _, bad := range []string{"", "   ", "not-a-provider:thing", "zzz-nothing"} {
		if _, err := resolveSubagentModel(config.Config{}, bad); err == nil {
			t.Errorf("resolveSubagentModel(%q) should fail", bad)
		}
	}
}

func TestSubagentFallbackOnlyOnQuotaOrAvailability(t *testing.T) {
	for _, message := range []string{
		"Your quota has been exhausted",
		"429 Too Many Requests",
		"insufficient credits",
		"the provider is unavailable (503)",
		"rate limit reached",
	} {
		if !subagentFallback(errors.New(message)) {
			t.Errorf("%q should allow a fallback", message)
		}
	}
	for _, message := range []string{
		"invalid_grant",
		"the model id is wrong",
		"context canceled",
	} {
		if subagentFallback(errors.New(message)) {
			t.Errorf("%q must not allow a fallback", message)
		}
	}
	if subagentFallback(nil) {
		t.Errorf("a nil error must not allow a fallback")
	}
}
