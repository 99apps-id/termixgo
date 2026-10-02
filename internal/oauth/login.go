package oauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// defaultRefreshLead is how early an access token is renewed, matching the
// 9router TOKEN_EXPIRY_BUFFER_MS. A provider can set a longer RefreshLead.
const defaultRefreshLead = 5 * time.Minute

// Login drives the flow a provider declares (device, codex, or pkce) and
// stores the token. It writes the code the operator must enter and the URL to
// open.
func Login(ctx context.Context, store *Store, provider string, out io.Writer) error {
	spec, ok := SpecFor(provider)
	if !ok {
		return fmt.Errorf("%s does not use an OAuth login", provider)
	}
	clock := Clock{}
	var token Token
	var err error
	switch spec.Kind {
	case "codex":
		flow := CodexFlow{ClientID: spec.ClientID, Issuer: spec.Issuer}
		device, startErr := StartCodexDevice(ctx, flow)
		if startErr != nil {
			return startErr
		}
		fmt.Fprintf(out, "Open %s and enter code %s\n", flow.VerifyURL(), device.UserCode)
		token, err = WaitCodexToken(ctx, flow, device, clock)
	case "pkce":
		flow := pkceFlowFromSpec(spec)
		if strings.TrimSpace(flow.ClientID) == "" {
			return fmt.Errorf("%s needs OAuth client credentials in %s", provider, spec.ClientIDEnv)
		}
		var session *pkceSession
		var authURL string
		session, authURL, err = StartPKCE(flow)
		if err == nil {
			fmt.Fprintf(out, "Open this URL to log in:\n%s\n", authURL)
			openBrowser(authURL)
			token, err = session.Wait(ctx, clock)
		}
	default:
		flow := DeviceFlow{ClientID: spec.ClientID, Scope: spec.Scope, DeviceURL: spec.DeviceURL, TokenURL: spec.TokenURL, VerifyHint: spec.VerifyHint}
		var code DeviceCode
		code, err = StartDevice(ctx, flow)
		if err == nil {
			target := code.VerificationURIComplete
			if strings.TrimSpace(target) == "" {
				target = code.VerificationURI
			}
			fmt.Fprintf(out, "Open %s and enter code %s\n", target, code.UserCode)
			token, err = WaitDevice(ctx, flow, code, clock)
		}
	}
	if err != nil {
		return err
	}
	token.LastRefresh = clock.now()
	if err := store.Save(provider, token); err != nil {
		return err
	}
	fmt.Fprintf(out, "Logged in to %s.\n", provider)
	return nil
}

// pkceFlowFromSpec maps a spec to the PKCE flow, resolving a client credential
// from the environment when the spec names one.
func pkceFlowFromSpec(spec Spec) PKCEFlow {
	clientID := spec.ClientID
	if spec.ClientIDEnv != "" {
		if value := strings.TrimSpace(os.Getenv(spec.ClientIDEnv)); value != "" {
			clientID = value
		}
	}
	clientSecret := spec.ClientSecret
	if spec.ClientSecretEnv != "" {
		if value := strings.TrimSpace(os.Getenv(spec.ClientSecretEnv)); value != "" {
			clientSecret = value
		}
	}
	return PKCEFlow{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AuthorizeURL: spec.AuthorizeURL,
		TokenURL:     spec.TokenURL,
		Scopes:       spec.Scopes,
		RedirectPort: spec.RedirectPort,
		RedirectPath: spec.RedirectPath,
		ExtraAuth:    spec.ExtraAuth,
		ExchangeJSON: spec.ExchangeJSON,
		RefreshJSON:  spec.RefreshJSON,
	}
}

// AccessToken returns a valid access token for a provider, refreshing and
// saving when the stored one has expired. It returns "" when no login exists.
// A credential the vendor has already aged out is dropped rather than sent,
// so the next action is a re-login instead of a stream of dead bearers.
func AccessToken(ctx context.Context, store *Store, provider string) string {
	token, ok := store.Load(provider)
	if !ok {
		return ""
	}
	spec, ok := SpecFor(provider)
	if !ok || strings.TrimSpace(token.Refresh) == "" {
		return token.Access
	}
	lead := spec.RefreshLead
	if lead <= 0 {
		lead = defaultRefreshLead
	}
	if token.Valid(time.Now(), lead) && !token.Stale(time.Now(), spec.MaxRefreshAge) {
		return token.Access
	}
	refreshed, err := refreshTokenOnce(ctx, store, spec, token)
	if err == nil && strings.TrimSpace(refreshed.Access) != "" {
		return refreshed.Access
	}
	var grant *GrantError
	if errors.As(err, &grant) {
		// The refresh token itself is dead (revoked, or reused after a
		// rotation). Keep sending its bearer only if the access token still
		// works; otherwise drop the login so HasKey, /status and the setup
		// wizard all agree the operator must log in again.
		if token.Valid(time.Now(), 0) {
			return token.Access
		}
		_ = store.Delete(provider)
		return ""
	}
	// A network hiccup or a vendor-side 5xx is not the credential's fault.
	// Hand back what we have and let the next call try again.
	return token.Access
}

// refreshFlights serialises refreshes per provider, the way 9router's
// withCredentialRefreshLock does. Refresh tokens rotate on use at these
// vendors, and replaying an already-rotated token can invalidate the whole
// session (the OpenAI comment on codex refreshLeadMs is blunt about it: a
// lost rotation logs the account out). Two goroutines that both found a token
// expired must therefore make one HTTP refresh, not two.
var refreshFlights sync.Map // provider -> *sync.Mutex

func refreshTokenOnce(ctx context.Context, store *Store, spec Spec, token Token) (Token, error) {
	lock, _ := refreshFlights.LoadOrStore(spec.Provider, new(sync.Mutex))
	lock.(*sync.Mutex).Lock()
	defer lock.(*sync.Mutex).Unlock()

	// Another caller may have refreshed while this one queued behind the
	// lock. Its saved token is now the good one; do not replay a rotated
	// refresh token against it.
	if current, ok := store.Load(spec.Provider); ok && current.Access != token.Access {
		lead := spec.RefreshLead
		if lead <= 0 {
			lead = defaultRefreshLead
		}
		if current.Valid(time.Now(), lead) {
			return current, nil
		}
		token = current
		if strings.TrimSpace(token.Refresh) == "" {
			return Token{}, fmt.Errorf("the stored %s login has no refresh token", spec.Provider)
		}
	}

	clock := Clock{}
	var refreshed Token
	var err error
	switch spec.Kind {
	case "codex":
		refreshed, err = RefreshCodex(ctx, CodexFlow{ClientID: spec.ClientID, Issuer: spec.Issuer}, token.Refresh, clock)
	case "pkce":
		refreshed, err = RefreshPKCE(ctx, pkceFlowFromSpec(spec), token.Refresh, clock)
	default:
		refreshed, err = RefreshDevice(ctx, DeviceFlow{ClientID: spec.ClientID, DeviceURL: spec.DeviceURL, TokenURL: spec.TokenURL}, token.Refresh, clock)
	}
	if err != nil || strings.TrimSpace(refreshed.Access) == "" {
		return Token{}, err
	}
	refreshed.LastRefresh = clock.now()
	if err := store.Save(spec.Provider, refreshed); err != nil {
		return Token{}, err
	}
	return refreshed, nil
}
