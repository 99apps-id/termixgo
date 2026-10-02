package oauth

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/secrets"
)

func noWaitClock() Clock {
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return Clock{
		Now:   func() time.Time { return fixed },
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
}

func TestDeviceFlow(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/device":
			fmt.Fprint(writer, `{"device_code":"dc","user_code":"WXYZ","verification_uri":"https://verify","interval":1,"expires_in":60}`)
		case "/token":
			polls++
			if polls == 1 {
				writer.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(writer, `{"error":"authorization_pending"}`)
				return
			}
			fmt.Fprint(writer, `{"access_token":"access-1","refresh_token":"refresh-1","expires_in":3600}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	flow := DeviceFlow{ClientID: "cid", Scope: "s", DeviceURL: server.URL + "/device", TokenURL: server.URL + "/token"}
	code, err := StartDevice(context.Background(), flow)
	if err != nil {
		t.Fatalf("StartDevice: %v", err)
	}
	if code.UserCode != "WXYZ" || code.VerificationURIComplete != "https://verify" {
		t.Fatalf("code = %+v", code)
	}
	token, err := WaitDevice(context.Background(), flow, code, noWaitClock())
	if err != nil {
		t.Fatalf("WaitDevice: %v", err)
	}
	if token.Access != "access-1" || token.Refresh != "refresh-1" || token.Expires.IsZero() {
		t.Fatalf("token = %+v", token)
	}
}

func TestRefreshDevice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		if request.Form.Get("grant_type") != "refresh_token" || request.Form.Get("refresh_token") != "old" {
			t.Errorf("refresh form = %v", request.Form)
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"access_token":"new-access","expires_in":1800}`)
	}))
	defer server.Close()

	token, err := RefreshDevice(context.Background(), DeviceFlow{ClientID: "cid", TokenURL: server.URL}, "old", noWaitClock())
	if err != nil {
		t.Fatalf("RefreshDevice: %v", err)
	}
	if token.Access != "new-access" || token.Refresh != "old" {
		t.Fatalf("token = %+v", token)
	}
}

func TestCodexDeviceFlow(t *testing.T) {
	polls := 0
	access := testJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct_123"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			fmt.Fprint(writer, `{"user_code":"CODE","device_auth_id":"DA","interval":1}`)
		case "/api/accounts/deviceauth/token":
			polls++
			if polls == 1 {
				writer.WriteHeader(http.StatusForbidden)
				fmt.Fprint(writer, `{}`)
				return
			}
			fmt.Fprint(writer, `{"authorization_code":"ac","code_verifier":"cv"}`)
		case "/oauth/token":
			fmt.Fprintf(writer, `{"access_token":%q,"refresh_token":"rt","id_token":"","expires_in":3600}`, access)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	flow := CodexFlow{ClientID: "app_test", Issuer: server.URL}
	device, err := StartCodexDevice(context.Background(), flow)
	if err != nil {
		t.Fatalf("StartCodexDevice: %v", err)
	}
	if device.UserCode != "CODE" || device.DeviceAuthID != "DA" {
		t.Fatalf("device = %+v", device)
	}
	token, err := WaitCodexToken(context.Background(), flow, device, noWaitClock())
	if err != nil {
		t.Fatalf("WaitCodexToken: %v", err)
	}
	if token.Access != access || token.AccountID != "acct_123" {
		t.Fatalf("token = %+v", token)
	}
}

func TestAccountIDFromJWT(t *testing.T) {
	token := testJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"abc"}}`)
	if got := AccountIDFromJWT(token); got != "abc" {
		t.Errorf("AccountIDFromJWT = %q, want abc", got)
	}
	if got := AccountIDFromJWT("not-a-jwt"); got != "" {
		t.Errorf("a malformed token should yield nothing, got %q", got)
	}
}

func TestTokenStore(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	store, err := secrets.Load()
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	tokens := NewStore(store)
	if _, ok := tokens.Load("xai-oauth"); ok {
		t.Fatalf("a fresh store should have no token")
	}
	if err := tokens.Save("xai-oauth", Token{Access: "a", Refresh: "r", Expires: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, ok := tokens.Load("xai-oauth")
	if !ok || loaded.Access != "a" || loaded.Refresh != "r" {
		t.Fatalf("Load = %+v ok=%v", loaded, ok)
	}
	if err := tokens.Delete("xai-oauth"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := tokens.Load("xai-oauth"); ok {
		t.Errorf("a deleted token should not load")
	}
}

// TestPKCEFlow drives the loopback authorization-code flow end to end against
// a fake token endpoint: start, visit the redirect with a code, exchange.
func TestPKCEFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/token" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"access_token":"pkce-access","refresh_token":"pkce-refresh","expires_in":3600}`)
	}))
	defer server.Close()

	flow := PKCEFlow{ClientID: "cid", AuthorizeURL: server.URL + "/authorize", TokenURL: server.URL + "/token", RedirectPath: "/callback"}
	session, authURL, err := StartPKCE(flow)
	if err != nil {
		t.Fatalf("StartPKCE: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	if parsed.Query().Get("code_challenge") == "" || parsed.Query().Get("code_challenge_method") != "S256" {
		t.Errorf("the authorize url must carry a PKCE challenge: %s", authURL)
	}
	redirect := parsed.Query().Get("redirect_uri")
	state := parsed.Query().Get("state")

	response, err := http.Get(redirect + "?code=the-code&state=" + state)
	if err != nil {
		t.Fatalf("call the callback: %v", err)
	}
	response.Body.Close()

	token, err := session.Wait(context.Background(), noWaitClock())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if token.Access != "pkce-access" || token.Refresh != "pkce-refresh" {
		t.Fatalf("token = %+v", token)
	}
}

// testJWT builds an unsigned JWT with the given JSON payload.
func testJWT(payload string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return header + "." + body + ".sig"
}
