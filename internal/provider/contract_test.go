package provider

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/oauth"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// storeAt builds a secret store in a throwaway state directory so the key
// tests never read or write the operator's real file.
func storeAt(t *testing.T) *secrets.Store {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	store, err := secrets.Load()
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	return store
}

func TestResolveKeyPrefersTheStoredSecret(t *testing.T) {
	store := storeAt(t)
	t.Setenv("ANTHROPIC_API_KEY", "from-environment")

	if got := ResolveKey(store, "anthropic"); got != "from-environment" {
		t.Errorf("with no stored key the environment is the fallback, got %q", got)
	}
	if err := store.Set(secrets.ProviderKey("anthropic"), "from-store"); err != nil {
		t.Fatalf("store key: %v", err)
	}
	if got := ResolveKey(store, "anthropic"); got != "from-store" {
		t.Errorf("the stored secret must win: the operator set it deliberately, got %q", got)
	}
}

func TestResolveKeyWithoutAStoreUsesTheEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", " env-key ")
	if got := ResolveKey(nil, "openai"); got != "env-key" {
		t.Errorf("key = %q, want the trimmed environment value", got)
	}
	if got := ResolveKey(nil, "not-a-provider"); got != "" {
		t.Errorf("an unknown provider has no key, got %q", got)
	}
}

func TestEnvKeyUsesEveryConfiguredNameInTurn(t *testing.T) {
	// Google accepts GEMINI_API_KEY or GOOGLE_API_KEY; either must work, and
	// the order in the provider table is the precedence.
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "second-choice")
	if got := EnvKey("google"); got != "second-choice" {
		t.Errorf("key = %q, want the second configured name", got)
	}
	t.Setenv("GEMINI_API_KEY", "first-choice")
	if got := EnvKey("google"); got != "first-choice" {
		t.Errorf("key = %q, want the first configured name to win", got)
	}
	// A blank variable is not a key.
	t.Setenv("GEMINI_API_KEY", "   ")
	if got := EnvKey("google"); got != "second-choice" {
		t.Errorf("a whitespace value must be ignored, got %q", got)
	}
}

func TestKeySourceNamesWhereTheKeyCameFrom(t *testing.T) {
	store := storeAt(t)
	if got := KeySource(store, "openai"); got != "" {
		t.Errorf("source = %q, want empty when there is no key", got)
	}
	t.Setenv("OPENAI_API_KEY", "env-key")
	if got := KeySource(store, "openai"); got != "environment" {
		t.Errorf("source = %q, want environment", got)
	}
	if err := store.Set(secrets.ProviderKey("openai"), "stored-key"); err != nil {
		t.Fatalf("store key: %v", err)
	}
	if got := KeySource(store, "openai"); got != "stored" {
		t.Errorf("source = %q, want stored", got)
	}
}

func TestKeySourceReportsAnOAuthLogin(t *testing.T) {
	store := storeAt(t)
	if got := KeySource(store, "xai-oauth"); got != "" {
		t.Errorf("source = %q, want empty before any login", got)
	}
	// A pasted API key must not impersonate a login: the status view has to
	// say where the credential really came from.
	if err := store.Set(secrets.ProviderKey("xai-oauth"), "stored-key"); err != nil {
		t.Fatalf("store key: %v", err)
	}
	if got := KeySource(store, "xai-oauth"); got != "" {
		t.Errorf("source = %q, want empty; an API key is not an OAuth login", got)
	}
	_ = store.Delete(secrets.ProviderKey("xai-oauth"))
	tokens := OAuthStore(store)
	if err := tokens.Save("xai-oauth", oauth.Token{Access: "a", Expires: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("save token: %v", err)
	}
	if got := KeySource(store, "xai-oauth"); got != "oauth login" {
		t.Errorf("source = %q, want the login to be visible in the status view", got)
	}
	if !HasKey(store, "xai-oauth") {
		t.Error("a stored login must satisfy the key requirement")
	}
	if err := tokens.Delete("xai-oauth"); err != nil {
		t.Fatalf("delete token: %v", err)
	}
	if got := KeySource(store, "xai-oauth"); got != "" {
		t.Errorf("source = %q, want empty after logout", got)
	}
	if HasKey(store, "xai-oauth") {
		t.Error("a logged-out provider must not report a key from the environment")
	}
}

func TestHasKeyFollowsTheProviderRequirement(t *testing.T) {
	store := storeAt(t)
	// The local providers are in the table but need no key, so they always
	// report one. That is what lets the wizard accept them immediately.
	for _, id := range []string{"ollama", "lmstudio", "mlx"} {
		if !HasKey(store, id) {
			t.Errorf("%s needs no key, so it always has one", id)
		}
	}
	if HasKey(store, "openai") {
		t.Errorf("openai needs a key and none is set")
	}
	if HasKey(store, "not-a-provider") {
		t.Errorf("an unknown provider cannot have a key")
	}
	t.Setenv("OPENAI_API_KEY", "env-key")
	if !HasKey(store, "openai") {
		t.Errorf("an environment key must count as a key")
	}
}

func TestResolverForIsBoundToTheStore(t *testing.T) {
	store := storeAt(t)
	t.Setenv("GROQ_API_KEY", "")
	resolve := ResolverFor(store)
	if got := resolve("groq"); got != "" {
		t.Errorf("key = %q, want empty before anything is stored", got)
	}
	if err := store.Set(secrets.ProviderKey("groq"), "gsk_test"); err != nil {
		t.Fatalf("store key: %v", err)
	}
	if got := resolve("groq"); got != "gsk_test" {
		t.Errorf("key = %q, want the stored value", got)
	}
}

func TestWireModelHonoursTheOverrideMap(t *testing.T) {
	model := Model{ID: "local-alias", Provider: "openai", APIID: "gpt-5.4"}

	if got := WireModel(config.Default(), model); got != "gpt-5.4" {
		t.Errorf("wire model = %q, want the API id", got)
	}
	cfg := config.Default()
	cfg.ModelOverrides = map[string]string{"local-alias": "vendor-2026-01-01"}
	if got := WireModel(cfg, model); got != "vendor-2026-01-01" {
		t.Errorf("wire model = %q, want the override", got)
	}
	// A blank override is not an override, otherwise clearing the field in the
	// config would send an empty model name.
	cfg.ModelOverrides = map[string]string{"local-alias": "   "}
	if got := WireModel(cfg, model); got != "gpt-5.4" {
		t.Errorf("wire model = %q, want it to fall back to the API id", got)
	}
	// With no API id the local id is what the provider knows.
	bare := Model{ID: "some-model", Provider: "openai"}
	if got := WireModel(config.Default(), bare); got != "some-model" {
		t.Errorf("wire model = %q, want the id itself", got)
	}
}

func TestCostModelSeparatesFreePricedAndUnknown(t *testing.T) {
	local := Model{ID: "qwen2.5-coder:latest", Provider: "ollama"}
	if price, known := local.CostModel(nil); !known || price.Known() {
		t.Errorf("a local model is known-free, got %+v (known %v)", price, known)
	}

	priced, ok := ModelByID("gpt-5.4-mini")
	if !ok {
		t.Fatalf("the catalogue should list gpt-5.4-mini")
	}
	price, known := priced.CostModel(nil)
	if !known || !price.Known() {
		t.Errorf("a catalogued model has a price, got %+v (known %v)", price, known)
	}

	unknown := Model{ID: "some-brand-new-model", Provider: "openai"}
	if _, known := unknown.CostModel(nil); known {
		t.Errorf("an unpriced model must report itself as unknown, not as free")
	}
	// An override is what makes a brand-new model budgetable.
	override := map[string]Pricing{"some-brand-new-model": {InputPerMillion: 2, OutputPerMillion: 6}}
	overridden, known := unknown.CostModel(override)
	if !known || overridden.InputPerMillion != 2 || overridden.OutputPerMillion != 6 {
		t.Errorf("override = %+v (known %v)", overridden, known)
	}
}

func TestIsLocalNamesTheLocalServers(t *testing.T) {
	for _, id := range []string{"ollama", "lmstudio", "mlx"} {
		if !IsLocal(id) {
			t.Errorf("%s runs on localhost and must count as local", id)
		}
	}
	for _, id := range []string{"openai", "anthropic", "openai-compatible", ""} {
		if IsLocal(id) {
			t.Errorf("%s is not a local server", id)
		}
	}
}

func TestModelsForKeepsCatalogueOrder(t *testing.T) {
	anthropic := ModelsFor("anthropic")
	if len(anthropic) == 0 {
		t.Fatalf("anthropic should have models")
	}
	for _, model := range anthropic {
		if model.Provider != "anthropic" {
			t.Errorf("ModelsFor leaked %s from %s", model.ID, model.Provider)
		}
	}
	// The order must match the catalogue, which is what the picker renders.
	var expected []string
	for _, m := range Models() {
		if m.Provider == "openai" {
			expected = append(expected, m.ID)
		}
	}
	if len(expected) == 0 {
		t.Fatal("openai should have models")
	}
	got := ModelsFor("openai")
	if len(got) != len(expected) {
		t.Fatalf("ModelsFor(openai) has %d models, the catalogue has %d", len(got), len(expected))
	}
	for i, id := range expected {
		if got[i].ID != id {
			t.Fatalf("openai model %d = %q, want catalogue order %q", i, got[i].ID, id)
		}
	}
	if got := ModelsFor("no-such-provider"); len(got) != 0 {
		t.Errorf("ModelsFor = %#v, want empty", got)
	}
}

func TestClientIDIsTheProviderID(t *testing.T) {
	client, err := NewClient("openai-compatible", "", func(string) string { return "" })
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if got := client.ID(); got != "openai-compatible" {
		t.Errorf("ID = %q, want the provider id", got)
	}
}

func TestUsageAddsEveryCounter(t *testing.T) {
	sum := Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}.
		Add(Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30})
	if sum.PromptTokens != 11 || sum.CompletionTokens != 22 || sum.TotalTokens != 33 {
		t.Errorf("sum = %+v, want 11/22/33", sum)
	}
}

func TestRetryErrorUnwrapsToTheStatusError(t *testing.T) {
	inner := errors.New("upstream is down")
	wrapped := &retryError{err: inner, after: 3 * time.Second}
	if got := wrapped.Unwrap(); got != inner {
		t.Errorf("Unwrap = %v, want the status error", got)
	}
	if wrapped.Error() != "upstream is down" {
		t.Errorf("Error = %q, want the inner message", wrapped.Error())
	}
	if !errors.Is(wrapped, inner) {
		t.Errorf("errors.Is should reach the inner error")
	}
}

// TestSecretsPathIsNotTheRealOne guards the test helper: a mistake here would
// write to the operator's actual key file.
func TestSecretsPathIsNotTheRealOne(t *testing.T) {
	store := storeAt(t)
	if err := store.Set(secrets.ProviderKey("openai"), "probe"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	home := os.Getenv(config.EnvHome)
	if home == "" {
		t.Fatalf("the test must run against a temporary state directory")
	}
	if _, err := os.Stat(filepath.Join(home, "secrets.json")); err != nil {
		t.Errorf("the store should be inside the temporary directory: %v", err)
	}
}
