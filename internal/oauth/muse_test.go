package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestMuseSessionPersistsWithoutPrematureExpiry pins that a minted Muse key does
// not get an artificial 1-hour expiration timestamp. The session persists
// indefinitely like Antigravity until revoked or logged out.
func TestMuseSessionPersistsWithoutPrematureExpiry(t *testing.T) {
	now := time.Now()
	if got := museExpiry(time.Time{}, now); !got.IsZero() {
		t.Errorf("a blank device expiry should persist with zero expiry, got %v", got)
	}
	future := now.Add(40 * time.Minute)
	if got := museExpiry(future, now); !got.IsZero() {
		t.Errorf("a server device expiry should not limit minted key lifetime, got %v", got)
	}
	if got := museExpiry(now.Add(-time.Minute), now); !got.IsZero() {
		t.Errorf("a past device expiry should still yield zero expiry, got %v", got)
	}
}

// TestMuseSpecUsesTheDeviceFlow pins the Meta endpoints and that the login is
// offered, since the device URL and the mint URL are easy to get wrong.
func TestMuseSpecUsesTheDeviceFlow(t *testing.T) {
	spec, ok := SpecFor("muse")
	if !ok {
		t.Fatal("muse is not a login provider")
	}
	if spec.Kind != "muse" {
		t.Errorf("kind = %q, want muse", spec.Kind)
	}
	if spec.ClientID != "1031625952748946" {
		t.Errorf("client id = %q", spec.ClientID)
	}
	if spec.DeviceURL != "https://auth.meta.com/oidc/device/authorization/" {
		t.Errorf("device url = %q", spec.DeviceURL)
	}
	if spec.TokenURL != "https://auth.meta.com/oidc/device/token/" {
		t.Errorf("token url = %q", spec.TokenURL)
	}
	if spec.MintURL != "https://api.meta.ai/muse-code/key" {
		t.Errorf("mint url = %q", spec.MintURL)
	}
	found := false
	for _, name := range Supported() {
		if name == "muse" {
			found = true
		}
	}
	if !found {
		t.Errorf("muse is missing from Supported(): %v", Supported())
	}
}

// TestMintMetaKeyExchangesTheDeviceToken proves the mint sends the dca token as
// a bearer, sets x-api-version, sends an empty JSON body, and returns the API
// key as the access token.
func TestMintMetaKeyExchangesTheDeviceToken(t *testing.T) {
	var gotAuth, gotAgent, gotAPIVersion string
	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotAuth = request.Header.Get("Authorization")
		gotAgent = request.Header.Get("User-Agent")
		gotAPIVersion = request.Header.Get("x-api-version")
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &gotBody)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"api_key":        "LLM|minted",
			"base_url":       "https://api.meta.ai/v1",
			"user_email":     "engineer@meta.com",
			"user_full_name": "Meta Engineer",
		})
	}))
	defer server.Close()

	token, err := MintMetaKey(context.Background(), server.URL, "dca:abc123")
	if err != nil {
		t.Fatalf("MintMetaKey: %v", err)
	}
	if gotAuth != "Bearer dca:abc123" {
		t.Errorf("Authorization = %q, want Bearer dca:abc123", gotAuth)
	}
	if gotAgent != metaUserAgent {
		t.Errorf("User-Agent = %q, want %q", gotAgent, metaUserAgent)
	}
	if gotAPIVersion != "1.0.0" {
		t.Errorf("x-api-version = %q, want 1.0.0", gotAPIVersion)
	}
	if len(gotBody) != 0 {
		t.Errorf("body = %v, want empty JSON {}", gotBody)
	}
	if token.Access != "LLM|minted" {
		t.Errorf("access = %q, want the minted key", token.Access)
	}
	if token.Refresh != "dca:abc123" {
		t.Errorf("refresh = %q, want the dca token", token.Refresh)
	}
}

func TestMintMetaKeyReportsFailures(t *testing.T) {
	if _, err := MintMetaKey(context.Background(), "http://unused.invalid", "   "); err == nil {
		t.Errorf("a blank device token must be reported")
	}

	reject := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":"invalid token"}`))
	}))
	defer reject.Close()
	if _, err := MintMetaKey(context.Background(), reject.URL, "dca:dead"); err == nil {
		t.Errorf("a rejected mint must be reported")
	}

	missing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"base_url":"https://api.meta.ai/v1"}`))
	}))
	defer missing.Close()
	if _, err := MintMetaKey(context.Background(), missing.URL, "dca:abc"); err == nil {
		t.Errorf("a response without an api_key must be reported")
	}
}

// TestMuseAccessTokenPersistsThroughPastExpiry proves that an existing Muse token
// with an old expired timestamp continues to yield its access token and does not
// get deleted from the store.
func TestMuseAccessTokenPersistsThroughPastExpiry(t *testing.T) {
	store := testStore(t)
	if err := store.Save("muse", Token{
		Access:  "LLM|persisted-key",
		Refresh: "dca:old",
		Expires: time.Now().Add(-2 * time.Hour), // Expired in the past
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := AccessToken(context.Background(), store, "muse")
	if got != "LLM|persisted-key" {
		t.Fatalf("AccessToken = %q, want LLM|persisted-key", got)
	}

	saved, ok := store.Load("muse")
	if !ok {
		t.Fatal("muse login must not be deleted from store")
	}
	if !saved.Expires.IsZero() {
		t.Errorf("saved expires should have been cleared to zero, got %v", saved.Expires)
	}
}

// TestMuseForceRefreshTokenPreservesTokenOnFailure proves that a failed refresh
// does not purge the active Muse access token from the store.
func TestMuseForceRefreshTokenPreservesTokenOnFailure(t *testing.T) {
	store := testStore(t)
	reject := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":"invalid_grant","error_description":"token has expired"}`))
	}))
	defer reject.Close()

	useTestSpec(t, Spec{
		Provider: "muse",
		Kind:     "muse",
		MintURL:  reject.URL,
	})

	if err := store.Save("muse", Token{
		Access:  "LLM|keep-alive",
		Refresh: "dca:stale",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := ForceRefreshToken(context.Background(), store, "muse")
	if got != "LLM|keep-alive" {
		t.Fatalf("ForceRefreshToken = %q, want LLM|keep-alive", got)
	}

	if _, ok := store.Load("muse"); !ok {
		t.Fatal("muse login must remain stored even after a failed refresh attempt")
	}
}

// TestMintMetaKeyAcceptsCamelCaseKey proves a mint response that names the key
// apiKey still logs in, so a server-side casing variant does not fail login.
func TestMintMetaKeyAcceptsCamelCaseKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]string{"apiKey": "LLM|camel"})
	}))
	defer server.Close()

	token, err := MintMetaKey(context.Background(), server.URL, "dca:abc123")
	if err != nil {
		t.Fatalf("MintMetaKey: %v", err)
	}
	if token.Access != "LLM|camel" {
		t.Errorf("access = %q, want LLM|camel", token.Access)
	}
	if token.Refresh != "dca:abc123" {
		t.Errorf("refresh = %q, want the dca token", token.Refresh)
	}
}

// TestMuseTokenFromDeviceMintsAndKeepsDca proves the success path stores the
// minted key with the durable dca token as refresh, never the device
// refresh, so a later 401 can re-mint without a new login.
func TestMuseTokenFromDeviceMintsAndKeepsDca(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]string{"api_key": "LLM|fresh"})
	}))
	defer server.Close()

	device := Token{Access: "dca:abc", Refresh: "other-refresh", Expires: time.Now().Add(time.Hour)}
	got, notice, err := museTokenFromDevice(context.Background(), server.URL, device, time.Now())
	if err != nil {
		t.Fatalf("museTokenFromDevice: %v", err)
	}
	if notice != "" {
		t.Errorf("notice = %q, want empty on success", notice)
	}
	if got.Access != "LLM|fresh" {
		t.Errorf("access = %q, want LLM|fresh", got.Access)
	}
	if got.Refresh != "dca:abc" {
		t.Errorf("refresh = %q, want the dca token, not the device refresh", got.Refresh)
	}
	if !got.Expires.IsZero() {
		t.Errorf("expires = %v, want zero so the session never expires locally", got.Expires)
	}
}

// TestMuseTokenFromDeviceKeepsDcaWhenMintFails proves a transient mint outage
// keeps a dca-only login instead of failing, so the key mints lazily before
// the first request and the operator stays logged in.
func TestMuseTokenFromDeviceKeepsDcaWhenMintFails(t *testing.T) {
	outage := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"error":"server_error"}`))
	}))
	defer outage.Close()

	device := Token{Access: "dca:abc", Refresh: "other-refresh"}
	got, notice, err := museTokenFromDevice(context.Background(), outage.URL, device, time.Now())
	if err != nil {
		t.Fatalf("a failed mint must not fail the login, got: %v", err)
	}
	if notice == "" {
		t.Error("notice should say the mint was deferred")
	}
	if got.Access != "" {
		t.Errorf("access = %q, want empty until the lazy mint", got.Access)
	}
	if got.Refresh != "dca:abc" {
		t.Errorf("refresh = %q, want the dca token", got.Refresh)
	}
	if !got.Expires.IsZero() {
		t.Errorf("expires = %v, want zero", got.Expires)
	}
}

// TestMuseAccessTokenLazyMintsFromDcaOnly proves a dca-only login mints on
// first use and persists the key without an expiry, which is what makes a
// deferred login succeed.
func TestMuseAccessTokenLazyMintsFromDcaOnly(t *testing.T) {
	mint := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]string{"api_key": "LLM|lazy"})
	}))
	defer mint.Close()
	useTestSpec(t, Spec{Provider: "muse", Kind: "muse", MintURL: mint.URL})

	store := testStore(t)
	if err := store.Save("muse", Token{Access: "", Refresh: "dca:abc"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := AccessToken(context.Background(), store, "muse"); got != "LLM|lazy" {
		t.Fatalf("AccessToken = %q, want LLM|lazy", got)
	}
	saved, ok := store.Load("muse")
	if !ok {
		t.Fatal("the lazy mint must be saved")
	}
	if saved.Access != "LLM|lazy" || saved.Refresh != "dca:abc" {
		t.Errorf("saved = %+v, want the minted key with the dca refresh", saved)
	}
	if !saved.Expires.IsZero() {
		t.Errorf("saved expires = %v, want zero", saved.Expires)
	}
}
