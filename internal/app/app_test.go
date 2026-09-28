package app

import (
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	workspace := t.TempDir()
	application, err := New(workspace)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return application
}

func TestNewNeedsSetupWithoutAModel(t *testing.T) {
	application := newTestApp(t)
	if !application.NeedsSetup() {
		t.Fatalf("a fresh install should need setup")
	}
	if application.Trusted() {
		t.Fatalf("a fresh folder should be untrusted")
	}
}

func TestSetTrustPersists(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetTrust(true); err != nil {
		t.Fatalf("SetTrust: %v", err)
	}
	if !application.Trusted() {
		t.Fatalf("Trusted should be true after SetTrust(true)")
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reloaded.IsTrusted(application.Workspace()) {
		t.Fatalf("trust was not persisted")
	}
}

func TestSetModelRequiresAKeyThenBuildsClient(t *testing.T) {
	application := newTestApp(t)

	if _, err := application.SetModelByQuery("claude-sonnet-4-5"); err == nil {
		t.Fatalf("switching to a keyed provider without a key must fail")
	}

	if err := application.Secrets().Set(secrets.ProviderKey("anthropic"), "sk-ant-test"); err != nil {
		t.Fatalf("store key: %v", err)
	}
	model, err := application.SetModelByQuery("claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("SetModelByQuery: %v", err)
	}
	if model.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", model.Provider)
	}
	if !application.HasModel() {
		t.Errorf("the client should be built once a key exists")
	}
	reloaded, _ := config.Load()
	if reloaded.DefaultModel != "claude-sonnet-4-5" {
		t.Errorf("the model choice was not persisted, got %q", reloaded.DefaultModel)
	}
}

func TestSetModelAcceptsLocalProviderWithoutKey(t *testing.T) {
	application := newTestApp(t)
	if _, err := application.SetModelByQuery("qwen2.5-coder:latest"); err != nil {
		t.Fatalf("a local model needs no key: %v", err)
	}
	if application.CurrentModel().Provider != "ollama" {
		t.Errorf("provider = %q, want ollama", application.CurrentModel().Provider)
	}
}

func TestSetModelAcceptsProviderQualifiedId(t *testing.T) {
	application := newTestApp(t)
	if err := application.Secrets().Set(secrets.ProviderKey("openai"), "sk-test"); err != nil {
		t.Fatal(err)
	}
	model, err := application.SetModelByQuery("openai:gpt-9-experimental")
	if err != nil {
		t.Fatalf("a provider-qualified custom model should be accepted: %v", err)
	}
	if model.ID != "gpt-9-experimental" || model.Provider != "openai" {
		t.Errorf("model = %+v", model)
	}
}

func TestSetApprovalModePersists(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetApprovalMode(config.ApprovalAsk); err != nil {
		t.Fatalf("SetApprovalMode: %v", err)
	}
	if application.Policy().Mode != "ask" {
		t.Errorf("policy mode = %q, want ask", application.Policy().Mode)
	}
	reloaded, _ := config.Load()
	if reloaded.ApprovalMode != config.ApprovalAsk {
		t.Errorf("approval mode was not persisted")
	}
	if err := application.SetApprovalMode("bogus"); err == nil {
		t.Errorf("an invalid mode must be rejected")
	}
}

func TestAllowToolPersistsAndSkipsApproval(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetApprovalMode(config.ApprovalAsk); err != nil {
		t.Fatal(err)
	}
	application.AllowTool("write_file")
	if application.Policy().NeedsApproval(&writeProbe{}) {
		t.Errorf("an always-allowed tool must not need approval")
	}
	reloaded, _ := config.Load()
	found := false
	for _, name := range reloaded.AlwaysAllowedTools {
		if name == "write_file" {
			found = true
		}
	}
	if !found {
		t.Errorf("always-allowed tools were not persisted: %v", reloaded.AlwaysAllowedTools)
	}
}

func TestStatusNamesWorkspaceAndTrust(t *testing.T) {
	application := newTestApp(t)
	status := application.Status()
	if !strings.Contains(status, application.Workspace()) {
		t.Errorf("status must name the workspace: %q", status)
	}
	if !strings.Contains(status, "untrusted") {
		t.Errorf("status must state the trust: %q", status)
	}
	if !strings.Contains(status, "telegram: off") {
		t.Errorf("status must report telegram: %q", status)
	}
}

func TestProviderCatalogueIsConsistent(t *testing.T) {
	for _, model := range provider.Models() {
		if _, ok := provider.ByID(model.Provider); !ok {
			t.Errorf("model %s names unknown provider %s", model.ID, model.Provider)
		}
	}
}

// writeProbe is a minimal Tool that reports as mutating and edit-risk.
type writeProbe struct{}

func (writeProbe) Name() string                    { return "write_file" }
func (writeProbe) Aliases() []string               { return nil }
func (writeProbe) Description() string             { return "probe" }
func (writeProbe) Schema() map[string]any          { return map[string]any{} }
func (writeProbe) Mutating() bool                  { return true }
func (writeProbe) Risk() agent.Risk                { return agent.RiskEdit }
func (writeProbe) Label(map[string]any) string     { return "Writing" }
func (writeProbe) DoneLabel(map[string]any) string { return "Wrote" }
func (writeProbe) Run(context.Context, *agent.Env, map[string]any) (agent.Result, error) {
	return agent.Result{}, nil
}
