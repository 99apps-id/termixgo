package oauth

import "sort"

// Spec describes how one provider logs in. Kind is "device" for RFC 8628,
// "codex" for the OpenAI Codex device flow, or "pkce" for an authorization
// code with PKCE and a loopback callback.
type Spec struct {
	Provider   string
	Kind       string
	ClientID   string
	Scope      string
	DeviceURL  string
	TokenURL   string
	VerifyHint string
	Issuer     string

	// PKCE fields. ClientSecret is the public installed-app secret some
	// vendors ship (Google), not a confidential secret. A secret that GitHub
	// secret scanning flags is read from the environment instead of being
	// committed, which is why the Google client has env names here.
	ClientSecret    string
	ClientIDEnv     string
	ClientSecretEnv string
	AuthorizeURL    string
	Scopes          []string
	RedirectPort    int
	RedirectPath    string
	ExtraAuth       map[string]string
	ExchangeJSON    bool
	RefreshJSON     bool
}

// specs are the providers Termixgo can log in to. The client ids are the
// public ones the vendor's own CLI ships, as extracted from Hermes and
// OpenCode on this machine.
var specs = map[string]Spec{
	"xai-oauth": {
		Provider:  "xai-oauth",
		Kind:      "device",
		ClientID:  "b1a00492-073a-47ea-816f-4c329264a828",
		Scope:     "openid profile email offline_access grok-cli:access api:access conversations:read conversations:write",
		DeviceURL: "https://auth.x.ai/oauth2/device/code",
		TokenURL:  "https://auth.x.ai/oauth2/token",
	},
	"openai-codex": {
		Provider: "openai-codex",
		Kind:     "codex",
		ClientID: "app_EMoamEEZ73f0CkXaXp7hrann",
		Issuer:   "https://auth.openai.com",
	},
	"claude-oauth": {
		Provider:     "claude-oauth",
		Kind:         "pkce",
		ClientID:     "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
		AuthorizeURL: "https://claude.ai/oauth/authorize",
		TokenURL:     "https://api.anthropic.com/v1/oauth/token",
		Scopes:       []string{"org:create_api_key", "user:profile", "user:inference"},
		RedirectPort: 54545,
		RedirectPath: "/callback",
		ExchangeJSON: true,
		RefreshJSON:  true,
	},
	// Antigravity shares Google's OAuth. The client id and the public
	// installed-app secret are read from the environment rather than committed,
	// because GitHub secret scanning flags the Google pair. Its inference is a
	// separate Cloud Code client and is not wired yet.
	"antigravity": {
		Provider:        "antigravity",
		Kind:            "pkce",
		ClientIDEnv:     "TERMIXGO_ANTIGRAVITY_CLIENT_ID",
		ClientSecretEnv: "TERMIXGO_ANTIGRAVITY_CLIENT_SECRET",
		AuthorizeURL:    "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:        "https://oauth2.googleapis.com/token",
		Scopes: []string{
			"https://www.googleapis.com/auth/cloud-platform",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
			"https://www.googleapis.com/auth/cclog",
			"https://www.googleapis.com/auth/experimentsandconfigs",
		},
		RedirectPath: "/auth/callback",
		ExtraAuth:    map[string]string{"access_type": "offline", "prompt": "consent"},
	},
}

// SpecFor returns the login spec for a provider.
func SpecFor(provider string) (Spec, bool) {
	spec, ok := specs[provider]
	return spec, ok
}

// Supported lists the providers with a login, sorted.
func Supported() []string {
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
