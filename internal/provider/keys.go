package provider

import (
	"context"
	"os"
	"strings"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/oauth"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// OAuthStore returns the token store bound to a secret store.
func OAuthStore(store *secrets.Store) *oauth.Store { return oauth.NewStore(store) }

// EnvKey returns a provider key from the environment, which lets CI and
// shell profiles work without touching the local store.
func EnvKey(id string) string {
	info, ok := ByID(id)
	if !ok {
		return ""
	}
	for _, name := range info.EnvKeys {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// ResolveKey returns the credential for a provider: an OAuth access token for
// a login provider, otherwise the stored secret, then the environment.
func ResolveKey(store *secrets.Store, id string) string {
	if info, ok := ByID(id); ok && info.OAuth {
		return oauth.AccessToken(context.Background(), oauth.NewStore(store), id)
	}
	if store != nil {
		if value := strings.TrimSpace(store.Get(secrets.ProviderKey(id))); value != "" {
			return value
		}
	}
	return EnvKey(id)
}

// UsesOAuth reports whether a provider logs in with a device code instead of
// taking an API key.
func UsesOAuth(id string) bool {
	info, ok := ByID(id)
	return ok && info.OAuth
}

// KeySource names where a key came from, for the status view.
func KeySource(store *secrets.Store, id string) string {
	if store != nil && store.Has(secrets.ProviderKey(id)) {
		return "stored"
	}
	if EnvKey(id) != "" {
		return "environment"
	}
	return ""
}

// HasKey reports whether a usable key exists for a provider.
func HasKey(store *secrets.Store, id string) bool {
	info, ok := ByID(id)
	if !ok {
		return false
	}
	if !info.NeedsKey {
		return true
	}
	return ResolveKey(store, id) != ""
}

// WireModel resolves the id sent on the wire, honouring the user override map
// so a renamed vendor model keeps a stable local id.
func WireModel(cfg config.Config, model Model) string {
	if override := strings.TrimSpace(cfg.ModelOverrides[model.ID]); override != "" {
		return override
	}
	return model.WireID()
}

// ResolverFor returns a KeyResolver bound to a secret store.
func ResolverFor(store *secrets.Store) KeyResolver {
	return func(id string) string { return ResolveKey(store, id) }
}

// DefaultBaseURL returns the endpoint used when the operator configures none.
// The custom-endpoint provider has no fixed host, so it falls back to the
// OpenAI base: a missing configuration then fails as a clear auth error
// instead of a missing-endpoint error.
func DefaultBaseURL(id string) string {
	info, ok := ByID(id)
	if !ok {
		return ""
	}
	if info.DefaultBaseURL != "" {
		return info.DefaultBaseURL
	}
	if id == "openai-compatible" {
		return "https://api.openai.com/v1"
	}
	return ""
}

// BaseURLFor resolves the endpoint for a provider: the user override first,
// then the provider default.
func BaseURLFor(cfg config.Config, id string) string {
	return cfg.BaseURL(id, DefaultBaseURL(id))
}
