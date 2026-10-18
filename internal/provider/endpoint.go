package provider

import (
	"fmt"
	"net/url"
	"strings"
)

// NeedsEndpoint reports whether a provider has no default host, so whoever is
// configuring it has to say where its server lives. A provider whose URL
// carries an account id (Cloudflare, Azure) or points at the operator's own
// machine (OpenAI Compatible) cannot have a working default.
//
// An unknown id answers false: a mistyped provider must not open a step that
// nothing can complete.
func NeedsEndpoint(providerID string) bool {
	info, ok := ByID(providerID)
	if !ok {
		return false
	}
	return strings.TrimSpace(info.DefaultBaseURL) == ""
}

// NormalizeBaseURL validates an operator-supplied base URL and tidies it.
//
// The one edit it makes is dropping a trailing slash, because a request path
// is joined onto the address and an address that already ends in a slash would
// double the separator. Everything else is rejected rather than guessed: a URL
// saved with a typo fails every request that follows, and the operator would
// have to go looking for the cause.
func NormalizeBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("type the server base URL, for example https://your-server/v1")
	}
	parsed, err := url.Parse(trimmed)
	// The host check runs before the scheme check: an address carried without
	// an authority is a bad URL whichever scheme it names, and that is the
	// message an operator can act on either way.
	if err != nil || strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("%q is not a URL with a host, for example https://your-server/v1", trimmed)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("the base URL must start with http:// or https://, got %q", trimmed)
	}
	return strings.TrimRight(trimmed, "/"), nil
}
