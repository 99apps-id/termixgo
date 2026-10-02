package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// metaDefaultMintURL is where a Meta Muse device token is exchanged for an LLM
// API key. The device grant yields a short-lived "dca:" token that the model
// endpoint rejects; the minted key is the credential that works.
const metaDefaultMintURL = "https://api.meta.ai/muse-code/key"

// metaUserAgent identifies the caller as the Muse CLI. Meta gates the mint and
// the Responses endpoint on this identity, so the official client string is
// reused rather than sending the Termixgo user agent.
const metaUserAgent = "muse-code/1.0.2"

// metaMintedKey is the subset of the mint response Termixgo needs.
type metaMintedKey struct {
	APIKey       string `json:"api_key"`
	BaseURL      string `json:"base_url"`
	UserEmail    string `json:"user_email"`
	UserFullName string `json:"user_full_name"`
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
	}, map[string]string{"dca_token": dcaToken})
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
	if strings.TrimSpace(minted.APIKey) == "" {
		return Token{}, fmt.Errorf("muse: the mint response had no api_key")
	}
	return Token{Access: minted.APIKey, Refresh: dcaToken}, nil
}
