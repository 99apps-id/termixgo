package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/secrets"
)

func newTokenStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	store, err := secrets.Load()
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	return NewStore(store)
}

// A login issues its token to the pair it used, so that pair must replace the
// one an earlier login stored: the older code kept the first pair forever and a
// later refresh then asked the wrong client.
func TestLoginReplacesTheStoredClientPair(t *testing.T) {
	tokens := newTokenStore(t)
	if err := tokens.SaveClient("antigravity", "old-id", "old-secret"); err != nil {
		t.Fatal(err)
	}
	saveLoginClient(tokens, "antigravity", PKCEFlow{ClientID: "new-id", ClientSecret: "new-secret"})
	if id, secret := tokens.LoadClient("antigravity"); id != "new-id" || secret != "new-secret" {
		t.Fatalf("stored = %q %q, want the login's pair", id, secret)
	}
	saveLoginClient(tokens, "antigravity", PKCEFlow{})
	if id, _ := tokens.LoadClient("antigravity"); id != "new-id" {
		t.Fatalf("a login with no client must leave the stored pair alone, got %q", id)
	}
}

// A refresh uses the pair stored at login, even when the build now carries
// another one, because only the issuing client can redeem a refresh token.
func TestRefreshUsesTheClientThatIssuedTheToken(t *testing.T) {
	tokens := newTokenStore(t)
	t.Setenv("TERMIXGO_ANTIGRAVITY_CLIENT_ID", "build-id")
	t.Setenv("TERMIXGO_ANTIGRAVITY_CLIENT_SECRET", "build-secret")
	spec, _ := SpecFor("antigravity")
	if flow := pkceFlowForRefresh(spec, tokens); flow.ClientID != "build-id" {
		t.Fatalf("with nothing stored the build's pair is used, got %q", flow.ClientID)
	}
	if err := tokens.SaveClient("antigravity", "issuer-id", "issuer-secret"); err != nil {
		t.Fatal(err)
	}
	flow := pkceFlowForRefresh(spec, tokens)
	if flow.ClientID != "issuer-id" || flow.ClientSecret != "issuer-secret" {
		t.Fatalf("flow = %q %q, want the pair stored at login", flow.ClientID, flow.ClientSecret)
	}
}

func TestAntigravityLoginAsksGoogleToOfferTheAccountChooser(t *testing.T) {
	spec, _ := SpecFor("antigravity")
	if got := spec.ExtraAuth["prompt"]; got != "consent select_account" {
		t.Fatalf("prompt = %q, want consent and the account chooser", got)
	}
}

func TestGoogleAccountEmailReadsTheSignedInAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer tok" {
			http.Error(writer, "no", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(writer, `{"email":"someone@example.com","id":"1"}`)
	}))
	defer server.Close()
	original := googleUserinfoURL
	googleUserinfoURL = server.URL
	t.Cleanup(func() { googleUserinfoURL = original })
	if got := googleAccountEmail(context.Background(), "tok"); got != "someone@example.com" {
		t.Errorf("email = %q", got)
	}
	if got := googleAccountEmail(context.Background(), "wrong"); got != "" {
		t.Errorf("a refused token must give no email, got %q", got)
	}
}

// TestRefreshFallsBackToTheBuildPairAndRemembersIt covers the login an older
// build left beside the wrong pair: the stored pair is refused, the pair this
// build carries is accepted, and it is stored so the next refresh goes straight
// through.
func TestRefreshFallsBackToTheBuildPairAndRemembersIt(t *testing.T) {
	store := newTokenStore(t)
	var tried []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		clientID := request.PostForm.Get("client_id")
		tried = append(tried, clientID)
		writer.Header().Set("Content-Type", "application/json")
		if clientID != "issuer" {
			writer.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(writer, `{"error":"invalid_client"}`)
			return
		}
		fmt.Fprint(writer, `{"access_token":"fresh","expires_in":3600}`)
	}))
	defer server.Close()
	useTestSpec(t, Spec{Provider: "heal-test", Kind: "pkce", ClientID: "issuer", ClientSecret: "s", TokenURL: server.URL})
	if err := store.SaveClient("heal-test", "stale-stored", "x"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("heal-test", Token{Access: "old", Refresh: "rt", Expires: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := AccessToken(context.Background(), store, "heal-test"); got != "fresh" {
		t.Fatalf("AccessToken = %q, want fresh", got)
	}
	if len(tried) != 2 || tried[0] != "stale-stored" || tried[1] != "issuer" {
		t.Errorf("clients tried = %v, want the stored pair first and then the build's", tried)
	}
	if id, _ := store.LoadClient("heal-test"); id != "issuer" {
		t.Errorf("stored client = %q, want the pair that worked", id)
	}
}
