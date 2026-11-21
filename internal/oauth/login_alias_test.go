package oauth

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// A token saved under an alias by an older build ("login claude" stored
// oauth:claude) is found by the provider that reads the canonical name.
func TestLegacyAliasTokenIsFound(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	store, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := NewStore(store).Save("claude", Token{Access: "acc"}); err != nil {
		t.Fatal(err)
	}
	token, ok := NewStore(store).Load("claude-oauth")
	if !ok || token.Access != "acc" {
		t.Fatalf("the alias token should be found: %+v %v", token, ok)
	}
}

// A login written by another process after this store loaded is seen by the
// running process without a restart.
func TestTokenFromAnotherProcessIsAdopted(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	secrets.SetAdoptIntervalForTest(t, 0)
	running, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := NewStore(running).Load("claude-oauth"); ok {
		t.Fatalf("no token yet")
	}
	other, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := NewStore(other).Save("claude-oauth", Token{Access: "fresh"}); err != nil {
		t.Fatal(err)
	}
	if token, ok := NewStore(running).Load("claude-oauth"); !ok || token.Access != "fresh" {
		t.Fatalf("the running store should adopt the new login: %+v %v", token, ok)
	}
}
