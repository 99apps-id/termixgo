package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
// a bearer and returns the API key as the access token, with the dca token kept
// as the refresh so a later 401 can re-mint without a new login.
func TestMintMetaKeyExchangesTheDeviceToken(t *testing.T) {
	var gotAuth, gotAgent string
	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotAuth = request.Header.Get("Authorization")
		gotAgent = request.Header.Get("User-Agent")
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
	if gotBody["dca_token"] != "dca:abc123" {
		t.Errorf("body = %v", gotBody)
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
