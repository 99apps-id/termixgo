package provider

import (
	"os"
	"testing"
)

// TestMain keeps the tests off the machine they run on: the identity lookup
// reads whatever Antigravity CLI is installed there.
func TestMain(m *testing.M) {
	agyIdentityLookup = func() (agyIdentity, bool) { return agyIdentity{}, false }
	os.Exit(m.Run())
}
