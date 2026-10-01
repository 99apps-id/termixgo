package oauth

import "sort"

// Spec describes how one provider logs in. Kind is "device" for RFC 8628 or
// "codex" for the OpenAI Codex device flow.
type Spec struct {
	Provider   string
	Kind       string
	ClientID   string
	Scope      string
	DeviceURL  string
	TokenURL   string
	VerifyHint string
	Issuer     string
}

// specs are the providers Termixgo can log in to. The client ids are the
// public ones the vendor's own CLI ships, as extracted from Hermes and
// OpenCode on this machine.
var specs = map[string]Spec{
	"xai-oauth": {
		Provider:  "xai-oauth",
		Kind:      "device",
		ClientID:  "b1a00492-073a-47ea-816f-4c329264a828",
		Scope:     "openid profile email offline_access grok-cli:access api:access",
		DeviceURL: "https://auth.x.ai/oauth2/device/code",
		TokenURL:  "https://auth.x.ai/oauth2/token",
	},
	"openai-codex": {
		Provider: "openai-codex",
		Kind:     "codex",
		ClientID: "app_EMoamEEZ73f0CkXaXp7hrann",
		Issuer:   "https://auth.openai.com",
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
