package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
// a bearer, sets x-api-version, enrolls the account with onboard:true, and
// returns the API key as the access token.
func TestMintMetaKeyExchangesTheDeviceToken(t *testing.T) {
	var gotAuth, gotAgent, gotAPIVersion string
	var gotBody map[string]any
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
	if len(gotBody) != 1 || gotBody["onboard"] != true {
		t.Errorf("body = %v, want {\"onboard\":true} to enroll the account", gotBody)
	}
	if token.Access != "LLM|minted" {
		t.Errorf("access = %q, want the minted key", token.Access)
	}
	if token.Refresh != "dca:abc123" {
		t.Errorf("refresh = %q, want the dca token", token.Refresh)
	}
	// The account the key belongs to is the fact an operator needs when two
	// machines behave differently on one subscription, so the mint has to carry
	// it out instead of dropping it with the rest of the identity fields.
	if token.AccountID != "engineer@meta.com" {
		t.Errorf("account = %q, want the email the mint reported", token.AccountID)
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

// fastMetaMintRetry removes the mint backoff so the retry test does not wait.
func fastMetaMintRetry(t *testing.T) {
	t.Helper()
	previous := metaMintRetryWait
	metaMintRetryWait = 0
	t.Cleanup(func() { metaMintRetryWait = previous })
}

// TestMintMetaKeyRetriesTransientFailures proves a rate-limited or flaky mint
// is replayed with the onboard flag instead of failing the login: the device
// code is one-shot, so giving up on the first 429 wastes the whole login.
func TestMintMetaKeyRetriesTransientFailures(t *testing.T) {
	fastMetaMintRetry(t)
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		if hits == 1 {
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]string{"api_key": "LLM|after-retry"})
	}))
	defer server.Close()

	token, err := MintMetaKey(context.Background(), server.URL, "dca:abc123")
	if err != nil {
		t.Fatalf("MintMetaKey: %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2 (one 429 then success)", hits)
	}
	if token.Access != "LLM|after-retry" || token.Refresh != "dca:abc123" {
		t.Errorf("token = %+v, want the retried key with the dca refresh", token)
	}
}

// TestMintMetaKeyReportsSubscriptionProblems proves an account without an
// active Muse Code subscription fails the login with an actionable message
// instead of minting a key the model endpoint answers with 404.
func TestMintMetaKeyReportsSubscriptionProblems(t *testing.T) {
	inactive := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"api_key":"LLM|dead","is_subs_active":false}`))
	}))
	defer inactive.Close()
	if _, err := MintMetaKey(context.Background(), inactive.URL, "dca:abc"); err == nil {
		t.Errorf("an inactive subscription must fail the login")
	} else if !strings.Contains(strings.ToLower(err.Error()), "inactive") {
		t.Errorf("error = %q, want it to name the inactive subscription", err)
	}

	unpaid := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"require_payment":true,"action_url":"https://muse.ai/pay"}`))
	}))
	defer unpaid.Close()
	if _, err := MintMetaKey(context.Background(), unpaid.URL, "dca:abc"); err == nil {
		t.Errorf("a subscription demand must fail the login")
	} else if !strings.Contains(err.Error(), "https://muse.ai/pay") {
		t.Errorf("error = %q, want it to carry the payment URL", err)
	}
}

// TestCheckMintSubscriptionRejectsUnusableAccounts pins the rejection without
// a network server: an inactive subscription or a payment demand fails even
// when a key is present, while a plain keyless body keeps the api_key error.
func TestCheckMintSubscriptionRejectsUnusableAccounts(t *testing.T) {
	inactive := false
	if err := checkMintSubscription(metaMintedKey{APIKey: "LLM|dead", IsSubsActive: &inactive}); err == nil {
		t.Errorf("an inactive subscription must be rejected")
	} else if !strings.Contains(strings.ToLower(err.Error()), "inactive") {
		t.Errorf("error = %q, want it to name the inactive subscription", err)
	}
	if err := checkMintSubscription(metaMintedKey{RequirePayment: true, ActionURL: "https://muse.ai/pay"}); err == nil {
		t.Errorf("a payment demand must be rejected")
	} else if !strings.Contains(err.Error(), "https://muse.ai/pay") {
		t.Errorf("error = %q, want it to carry the payment URL", err)
	}
	if err := checkMintSubscription(metaMintedKey{}); err == nil {
		t.Errorf("a keyless response must be rejected")
	} else if !strings.Contains(err.Error(), "no api_key") {
		t.Errorf("error = %q, want the missing-key message", err)
	}
	active := true
	if err := checkMintSubscription(metaMintedKey{APIKey: "LLM|ok", IsSubsActive: &active}); err != nil {
		t.Errorf("an active subscription with a key must pass, got %v", err)
	}
	if err := checkMintSubscription(metaMintedKey{APIKey: "LLM|ok"}); err != nil {
		t.Errorf("a key without subscription signals must pass, got %v", err)
	}
}
