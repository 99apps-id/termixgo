package provider

import (
	"strings"
	"testing"
)

// TestNormalizeBaseURLAcceptsAUsableAddress is the happy path the wizard
// depends on. A trailing slash is the one edit made, because joining a request
// path onto an address that already ends in one would double it.
func TestNormalizeBaseURLAcceptsAUsableAddress(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"https", "https://your-server/v1", "https://your-server/v1"},
		{"trailing slash", "https://your-server/v1/", "https://your-server/v1"},
		{"bare host", "https://your-server", "https://your-server"},
		{"plain http", "http://localhost:11434/v1", "http://localhost:11434/v1"},
		{"surrounding space", "  https://your-server/v1  ", "https://your-server/v1"},
		{"ip and port", "http://192.168.1.10:8080/v1", "http://192.168.1.10:8080/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeBaseURL(tc.in)
			if err != nil {
				t.Fatalf("NormalizeBaseURL(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeBaseURLRejectsWhatCannotWork checks that a typo is caught at the
// prompt rather than saved and then failed by every later request. Each message
// has to name the fix, which is why the text is asserted and not just the error.
func TestNormalizeBaseURLRejectsWhatCannotWork(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "type the server base URL"},
		{"only space", "   ", "type the server base URL"},
		{"no scheme", "your-server/v1", "is not a URL"},
		{"words", "not a url", "is not a URL"},
		{"scheme only", "https://", "is not a URL"},
		{"hostless", "https:///v1", "is not a URL"},
		// The host check runs before the scheme check, so a scheme carried
		// without an authority is reported as a bad URL. That is the message an
		// operator can act on either way.
		{"file url has no host", "file:///etc/passwd", "is not a URL"},
		{"other protocol", "ftp://your-server/v1", "must start with http:// or https://"},
		{"websocket", "ws://your-server/v1", "must start with http:// or https://"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeBaseURL(tc.in)
			if err == nil {
				t.Fatalf("NormalizeBaseURL(%q) = %q, want an error", tc.in, got)
			}
			if got != "" {
				t.Errorf("a rejected endpoint must not be returned, got %q", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error for %q = %q, want it to contain %q", tc.in, err.Error(), tc.want)
			}
		})
	}
}

// TestNeedsEndpointFindsTheProvidersWithoutAFixedHost pins the two halves of
// the decision the wizard makes on every provider: the three whose URL carries
// an account id have no default, and the local servers do, so they must never
// be asked where they live.
func TestNeedsEndpointFindsTheProvidersWithoutAFixedHost(t *testing.T) {
	for _, id := range []string{"openai-compatible", "cloudflare", "azure"} {
		if !NeedsEndpoint(id) {
			t.Errorf("%s has no default host, so the wizard must ask for one", id)
		}
	}
	for _, id := range []string{"openai", "anthropic", "google", "ollama", "lmstudio", "mlx"} {
		if NeedsEndpoint(id) {
			t.Errorf("%s has a fixed host, so the wizard must not ask", id)
		}
	}
}

// TestEveryProviderWithoutADefaultIsEitherAskedOrKeyed guards the registry
// itself: a provider added later with no default host and no key would reach
// the client with an empty address.
func TestEveryProviderWithoutADefaultIsEitherAskedOrKeyed(t *testing.T) {
	for _, info := range Providers() {
		if !NeedsEndpoint(info.ID) {
			continue
		}
		if !info.NeedsKey && info.ID != "openai-compatible" {
			t.Errorf("%s has no host and no key, so nothing would make its address valid", info.ID)
		}
	}
}

// TestNeedsEndpointIsFalseForAnUnknownProvider keeps a mistyped id from opening
// a step that nothing can complete.
func TestNeedsEndpointIsFalseForAnUnknownProvider(t *testing.T) {
	if NeedsEndpoint("not-a-provider") {
		t.Errorf("an unknown provider has no endpoint to ask for")
	}
}
