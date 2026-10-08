package provider

import (
	"errors"
	"testing"
)

// TestAuthHeuristicIgnoresQuotaWording pins the narrowed auth match: a quota
// error that mentions tokens must not trigger a key refresh and a retry, while
// a genuinely expired credential still does.
func TestAuthHeuristicIgnoresQuotaWording(t *testing.T) {
	if isAuthOrSessionError(errors.New("input token limit exceeded")) {
		t.Errorf("a token-limit error is quota, not auth, so it must not refresh the key")
	}
	for _, message := range []string{
		"401 unauthorized",
		"session expired, please login again",
		"token expired",
		"invalid token, refresh required",
	} {
		if !isAuthOrSessionError(errors.New(message)) {
			t.Errorf("%q is an auth failure, so it must refresh the key", message)
		}
		if !isMuseAuthOrSessionError(errors.New(message)) {
			t.Errorf("muse %q is an auth failure, so it must refresh the key", message)
		}
	}
	if isMuseAuthOrSessionError(errors.New("output token limit exceeded")) {
		t.Errorf("a muse token-limit error is quota, not auth, so it must not refresh the key")
	}
}
