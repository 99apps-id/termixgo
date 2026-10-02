package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// museExpiry returns the expiration timestamp for a minted Muse key.
// The device grant yields a durable dca token with no refresh grant, and the
// minted inference key is stable for the account with no advertised expiry.
// The session persists indefinitely and is renewed on demand from the stored
// dca token, so it returns a zero time.Time and never expires locally.
func museExpiry(deviceExpiry, now time.Time) time.Time {
	return time.Time{}
}

// metaDefaultMintURL is where a Meta Muse device token is exchanged for an LLM
// API key. The device grant yields a durable "dca:" token that the model
// endpoint rejects; the minted key is the credential that works.
const metaDefaultMintURL = "https://api.meta.ai/muse-code/key"

// metaUserAgent identifies the caller as the Muse CLI. Meta gates the mint and
// the Responses endpoint on this identity, so the official client string is
// reused rather than sending the Termixgo user agent.
const metaUserAgent = "muse-code/1.0.2"

// metaMintedKey is the subset of the mint response Termixgo needs.
type metaMintedKey struct {
	APIKey       string `json:"api_key"`
	APIKeyCamel  string `json:"apiKey"`
	Key          string `json:"key"`
	BaseURL      string `json:"base_url"`
	UserEmail    string `json:"user_email"`
	UserFullName string `json:"user_full_name"`
}

// key returns the minted credential under any name the server uses.
func (m metaMintedKey) key() string {
	for _, value := range []string{m.APIKey, m.APIKeyCamel, m.Key} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// MintMetaKey exchanges a Meta device (dca) token for the API key the Responses
// endpoint accepts.
//
// The key becomes the stored access token and the dca token is kept as the
// refresh, so a revoked or expired key can be re-minted without another login.
func MintMetaKey(ctx context.Context, mintURL, dcaToken string) (Token, error) {
	dcaToken = strings.TrimSpace(dcaToken)
	if dcaToken == "" {
		return Token{}, fmt.Errorf("muse: missing device token")
	}
	if strings.TrimSpace(mintURL) == "" {
		mintURL = metaDefaultMintURL
	}
	body, status, err := requestJSON(ctx, http.MethodPost, mintURL, map[string]string{
		"Authorization": "Bearer " + dcaToken,
		"User-Agent":    metaUserAgent,
		"x-api-version": "1.0.0",
	}, map[string]string{})
	if err != nil {
		return Token{}, fmt.Errorf("muse: mint request failed: %w", err)
	}
	if status >= 300 {
		return Token{}, statusError(mintURL, status, body)
	}
	var minted metaMintedKey
	if err := json.Unmarshal(body, &minted); err != nil {
		return Token{}, fmt.Errorf("muse: read mint response: %v", err)
	}
	if minted.key() == "" {
		return Token{}, fmt.Errorf("muse: the mint response had no api_key")
	}
	return Token{Access: minted.key(), Refresh: dcaToken}, nil
}

// museTokenFromDevice builds the stored Muse credential from a completed
// device grant. Minting is best effort: when the mint fails the durable dca
// token is kept as a dca-only login and the key is minted lazily before the
// first request, so a transient mint outage does not fail the login.
func museTokenFromDevice(ctx context.Context, mintURL string, device Token, now time.Time) (Token, string, error) {
	dca := strings.TrimSpace(device.Access)
	if dca == "" {
		dca = strings.TrimSpace(device.Refresh)
	}
	if dca == "" {
		return Token{}, "", fmt.Errorf("muse: the device grant returned no token")
	}
	minted, err := MintMetaKey(ctx, mintURL, dca)
	if err == nil {
		minted.Expires = museExpiry(device.Expires, now)
		minted.Refresh = dca
		return minted, "", nil
	}
	deferred := Token{Access: "", Refresh: dca, Expires: time.Time{}}
	return deferred, fmt.Sprintf("Muse key mint deferred (%v); will mint on first use.", err), nil
}
