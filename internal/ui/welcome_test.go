package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// TestWelcomeBlockFollowsTheModelChange is the fix for the stale opening screen:
// the welcome block was built once at startup, so a model change during
// onboarding or from /model left the old name in the transcript next to a header
// showing the new one.
func TestWelcomeBlockFollowsTheModelChange(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())

	cfg := config.Default()
	cfg.DefaultModel = "qwen2.5-coder:latest"
	cfg.ApprovalMode = config.ApprovalAll
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	application := testApp(t)
	if err := application.Secrets().Set(secrets.ProviderKey("openai"), "sk-test"); err != nil {
		t.Fatalf("store key: %v", err)
	}
	model := New(application)
	resize(model, 120, 40)

	if before := welcomeText(t, model); !strings.Contains(before, "Qwen2.5 Coder") {
		t.Fatalf("the initial welcome should name the model:\n%s", before)
	}

	next, _ := model.runSlash("model", "openai:gpt-9-custom")
	updated, ok := next.(*Model)
	if !ok {
		t.Fatalf("runSlash returned %T", next)
	}

	after := welcomeText(t, updated)
	if strings.Contains(after, "Qwen2.5") {
		t.Errorf("the welcome still names the old model:\n%s", after)
	}
	if !strings.Contains(after, "gpt-9-custom") {
		t.Errorf("the welcome should name the new model:\n%s", after)
	}
}

// TestWelcomeBlockFollowsTheTrustChange covers the other line the block holds.
func TestWelcomeBlockFollowsTheTrustChange(t *testing.T) {
	model := chatModel(t)
	if !strings.Contains(welcomeText(t, model), "trusted") {
		t.Fatalf("the fixture should show the trust state")
	}
	if err := model.app.SetTrust(false); err != nil {
		t.Fatalf("SetTrust: %v", err)
	}
	model.refreshWelcome()
	if !strings.Contains(welcomeText(t, model), "untrusted") {
		t.Errorf("the welcome did not follow the trust change:\n%s", welcomeText(t, model))
	}
}

func welcomeText(t *testing.T, model *Model) string {
	t.Helper()
	for _, item := range model.blocks {
		if item.kind == blockWelcome {
			return item.text
		}
	}
	t.Fatalf("no welcome block was found")
	return ""
}
