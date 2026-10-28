package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// newApp builds an app in the given workspace and registers its shutdown.
//
// Shutdown matters on Windows: the full-text index and the MCP pool hold open
// handles, and a test's temp directory cannot be removed while they are open.
// Registering the cleanup here rather than at each call site is what keeps a
// new test from forgetting it.
func newApp(t *testing.T, workspace string) (*App, error) {
	t.Helper()
	application, err := New(workspace)
	if err == nil {
		t.Cleanup(application.Shutdown)
	}
	return application, err
}

// newTestApp builds an app with a private state directory and workspace.
func newTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	application, err := newApp(t, t.TempDir())
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

// TestModelErrorNamesTheMissingKey is the serve fix: a configured model whose
// key is missing used to fail a Telegram run with a bare "no model is
// configured" while /model showed the id. The real reason must be kept.
func TestModelErrorNamesTheMissingKey(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	cfg.DefaultModel = "stepfun:step-3.7-flash"
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if application.HasModel() {
		t.Fatalf("no client should be built without a key")
	}
	problem := application.ModelError()
	if problem == nil {
		t.Fatalf("a configured model with no key must record the reason")
	}
	if !strings.Contains(problem.Error(), "StepFun") {
		t.Errorf("the reason should name the provider, got %q", problem.Error())
	}

	_, runErr := application.RunPrompt(context.Background(), "hello", nil)
	if !errors.Is(runErr, ErrNoModel) {
		t.Fatalf("run error = %v, want ErrNoModel", runErr)
	}
	if !strings.Contains(runErr.Error(), problem.Error()) {
		t.Errorf("the run error should carry the cause, got %v", runErr)
	}
	if !strings.Contains(application.Status(), "model problem:") {
		t.Errorf("/status should report the model problem:\n%s", application.Status())
	}
}

// TestGuessProviderPrefersAConfiguredEndpoint covers an unknown model id on a
// custom OpenAI-compatible endpoint: without this it fell through to a keyed
// provider and reported a missing key.
func TestGuessProviderPrefersAConfiguredEndpoint(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	cfg.BaseURLs["openai-compatible"] = "https://my-server/v1"
	cfg.DefaultModel = "my-local-model"
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	application, err := newApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !application.HasModel() {
		t.Fatalf("openai-compatible needs no key, so a client should be built: %v", application.ModelError())
	}
	if got := application.CurrentModel().Provider; got != "openai-compatible" {
		t.Errorf("provider = %q, want openai-compatible", got)
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
	} else {
		// The rejection is the only list of choices an operator sees, so it has
		// to be derived from the modes this build accepts rather than written
		// out by hand: plan was valid here while the message still named three.
		for _, mode := range config.ApprovalModes {
			if !strings.Contains(err.Error(), string(mode)) {
				t.Errorf("the rejection should name %q, got %q", mode, err)
			}
		}
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

func TestModelAliasResolutionAndAvailableModels(t *testing.T) {
	application := newTestApp(t)

	if got := application.resolveModelAlias("muse"); got != "muse-spark-1.3" {
		t.Errorf("muse alias = %q, want muse-spark-1.3", got)
	}
	if got := application.resolveModelAlias("stepfun"); got != "step-3.7-flash" && got != "stepfun-plan/step-3.7-flash" {
		t.Errorf("stepfun alias = %q", got)
	}
	if got := application.resolveModelAlias("antigravity"); got != "gemini-3.8-flash" && got != "antigravity-gemini-3.8-flash" {
		t.Errorf("antigravity alias = %q", got)
	}

	text := application.AvailableModelsText()
	if text == "" {
		t.Errorf("AvailableModelsText should not be empty")
	}
}

func TestOpenAICompatibleProviderAppearsInActiveProvidersAndModels(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetBaseURL("openai-compatible", "https://ollama.com/v1"); err != nil {
		t.Fatalf("SetBaseURL: %v", err)
	}
	if err := application.Secrets().Set(secrets.ProviderKey("openai-compatible"), "ollama-cloud-key"); err != nil {
		t.Fatalf("Set secret: %v", err)
	}
	selected, err := application.SetModelByQuery("openai-compatible:qwen3-coder:480b-cloud")
	if err != nil {
		t.Fatalf("SetModelByQuery: %v", err)
	}
	if selected.Provider != "openai-compatible" || selected.ID != "qwen3-coder:480b-cloud" {
		t.Fatalf("selected = %+v", selected)
	}

	// 1. Must appear in ActiveProviders even after switching to another provider
	if _, err := application.SetModelByQuery("ollama:llama3.2:latest"); err != nil {
		t.Fatalf("switch to ollama: %v", err)
	}
	foundProv := false
	for _, p := range application.ActiveProviders() {
		if p.ID == "openai-compatible" {
			foundProv = true
			break
		}
	}
	if !foundProv {
		t.Fatalf("openai-compatible must appear in ActiveProviders when configured")
	}

	// 2. ModelsForProvider("openai-compatible") must include the custom model
	models := application.ModelsForProvider("openai-compatible")
	foundModel := false
	for _, m := range models {
		if m.ID == "qwen3-coder:480b-cloud" {
			foundModel = true
			break
		}
	}
	if !foundModel {
		t.Fatalf("ModelsForProvider(openai-compatible) = %+v, missing qwen3-coder:480b-cloud", models)
	}

	// 3. AvailableModelsText (/models) must list OpenAI Compatible and the custom model
	text := application.AvailableModelsText("id")
	if !strings.Contains(text, "OpenAI Compatible (openai-compatible)") || !strings.Contains(text, "qwen3-coder:480b-cloud") {
		t.Errorf("AvailableModelsText missing openai-compatible model:\n%s", text)
	}
}
