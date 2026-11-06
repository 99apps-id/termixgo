package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// TestNamedCompatibleEndpointsCoexist proves each openai-compatible/<name>
// endpoint is its own provider with its own URL, key and models, so
// registering a second endpoint never overwrites the first.
func TestNamedCompatibleEndpointsCoexist(t *testing.T) {
	application := newTestApp(t)
	if err := application.SetBaseURL("openai-compatible/alpha", "https://alpha.example/v1"); err != nil {
		t.Fatalf("SetBaseURL alpha: %v", err)
	}
	if err := application.SetBaseURL("openai-compatible/beta", "https://beta.example/v1"); err != nil {
		t.Fatalf("SetBaseURL beta: %v", err)
	}
	if err := application.Secrets().Set(secrets.ProviderKey("openai-compatible/alpha"), "alpha-key"); err != nil {
		t.Fatalf("set alpha key: %v", err)
	}

	present := map[string]bool{}
	for _, info := range application.ActiveProviders() {
		if provider.IsCompatible(info.ID) {
			present[info.ID] = true
		}
	}
	for _, id := range []string{"openai-compatible/alpha", "openai-compatible/beta"} {
		if !present[id] {
			t.Errorf("ActiveProviders is missing %q", id)
		}
	}

	if _, err := application.SetModelByQuery("openai-compatible/alpha:custom-alpha-model"); err != nil {
		t.Fatalf("select alpha model: %v", err)
	}
	if models := application.ModelsForProvider("openai-compatible/alpha"); len(models) == 0 {
		t.Fatalf("ModelsForProvider(openai-compatible/alpha) is empty")
	}
	// A model on one endpoint must not leak into the other.
	for _, model := range application.ModelsForProvider("openai-compatible/beta") {
		if model.ID == "custom-alpha-model" {
			t.Errorf("alpha model leaked into the beta endpoint")
		}
	}
}
