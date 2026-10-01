package oauth

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// Login drives the device flow for a provider and stores the token. It writes
// the code the operator must enter and the URL to open.
func Login(ctx context.Context, store *Store, provider string, out io.Writer) error {
	spec, ok := SpecFor(provider)
	if !ok {
		return fmt.Errorf("%s does not use an OAuth login", provider)
	}
	clock := Clock{}
	switch spec.Kind {
	case "codex":
		flow := CodexFlow{ClientID: spec.ClientID, Issuer: spec.Issuer}
		device, err := StartCodexDevice(ctx, flow)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Open %s and enter code %s\n", flow.VerifyURL(), device.UserCode)
		token, err := WaitCodexToken(ctx, flow, device, clock)
		if err != nil {
			return err
		}
		if err := store.Save(provider, token); err != nil {
			return err
		}
		fmt.Fprintf(out, "Logged in to %s.\n", provider)
		return nil
	default:
		flow := DeviceFlow{ClientID: spec.ClientID, Scope: spec.Scope, DeviceURL: spec.DeviceURL, TokenURL: spec.TokenURL, VerifyHint: spec.VerifyHint}
		code, err := StartDevice(ctx, flow)
		if err != nil {
			return err
		}
		target := code.VerificationURIComplete
		if strings.TrimSpace(target) == "" {
			target = code.VerificationURI
		}
		fmt.Fprintf(out, "Open %s and enter code %s\n", target, code.UserCode)
		token, err := WaitDevice(ctx, flow, code, clock)
		if err != nil {
			return err
		}
		if err := store.Save(provider, token); err != nil {
			return err
		}
		fmt.Fprintf(out, "Logged in to %s.\n", provider)
		return nil
	}
}

// AccessToken returns a valid access token for a provider, refreshing and
// saving when the stored one has expired. It returns "" when no login exists.
func AccessToken(ctx context.Context, store *Store, provider string) string {
	token, ok := store.Load(provider)
	if !ok {
		return ""
	}
	if token.Valid(time.Now(), 2*time.Minute) {
		return token.Access
	}
	spec, ok := SpecFor(provider)
	if !ok || strings.TrimSpace(token.Refresh) == "" {
		return token.Access
	}
	clock := Clock{}
	var refreshed Token
	var err error
	switch spec.Kind {
	case "codex":
		refreshed, err = RefreshCodex(ctx, CodexFlow{ClientID: spec.ClientID, Issuer: spec.Issuer}, token.Refresh, clock)
	default:
		refreshed, err = RefreshDevice(ctx, DeviceFlow{ClientID: spec.ClientID, DeviceURL: spec.DeviceURL, TokenURL: spec.TokenURL}, token.Refresh, clock)
	}
	if err != nil || strings.TrimSpace(refreshed.Access) == "" {
		return token.Access
	}
	_ = store.Save(provider, refreshed)
	return refreshed.Access
}
