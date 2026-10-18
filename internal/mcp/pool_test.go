package mcp

import (
	"context"
	"strings"
	"testing"
)

// connectFake builds a pool with one fake server in a mode.
func connectFake(t *testing.T, mode string) *Pool {
	t.Helper()
	pool := NewPool()
	t.Cleanup(pool.Close)
	pool.Connect(t.Context(), nil, []Options{fakeOptions(mode)})
	return pool
}

// names lists the qualified names the pool produced, in order.
func names(bound []Bound) []string {
	out := make([]string, 0, len(bound))
	for _, item := range bound {
		out = append(out, item.Name)
	}
	return out
}

// TestConnectBindsQualifiedToolNames is the naming contract: a contributed tool
// carries the reserved prefix and the server it came from, so the operator can
// see where a call went and a server cannot shadow a built-in.
func TestConnectBindsQualifiedToolNames(t *testing.T) {
	pool := connectFake(t, modeNormal)

	got := names(pool.Tools())
	want := []string{"mcp_fake__echo", "mcp_fake__write_it"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tools = %v, want %v", got, want)
	}
	for _, name := range got {
		if !strings.HasPrefix(name, ReservedPrefix) {
			t.Errorf("%q must carry the reserved prefix", name)
		}
	}
	if len(pool.Failures()) != 0 {
		t.Errorf("a healthy server must not be recorded as a failure: %+v", pool.Failures())
	}
}

// TestNoContributedNameCanShadowABuiltin is the security rule behind the
// prefix: the registry indexes by name, so an unprefixed tool would let a
// server take over a built-in call.
func TestNoContributedNameCanShadowABuiltin(t *testing.T) {
	builtins := []string{"write_file", "run_command", "edit", "read_file", "delete_file"}
	pool := connectFake(t, modeNormal)

	for _, bound := range pool.Tools() {
		for _, builtin := range builtins {
			if bound.Name == builtin {
				t.Errorf("%s shadows the built-in %s", bound.Name, builtin)
			}
		}
	}
}

// TestServerNamesAreSanitizedForProviders keeps a name with spaces or slashes
// from producing a tool the provider rejects.
func TestServerNamesAreSanitizedForProviders(t *testing.T) {
	pool := NewPool()
	t.Cleanup(pool.Close)
	options := fakeOptions(modeNormal)
	options.Name = "my server/v2"
	pool.Connect(t.Context(), nil, []Options{options})

	got := names(pool.Tools())
	if len(got) == 0 {
		t.Fatalf("the server contributed nothing")
	}
	if got[0] != "mcp_my_server_v2__echo" {
		t.Errorf("name = %q, want the illegal characters folded to underscores", got[0])
	}
}

// TestAnUglyToolNameIsMadeLegal covers the server side of the same rule, which
// the pentest kit's style of names makes likely.
func TestAnUglyToolNameIsMadeLegal(t *testing.T) {
	pool := connectFake(t, modeUglyToolName)

	got := names(pool.Tools())
	if len(got) != 1 {
		t.Fatalf("tools = %v, want one", got)
	}
	for _, symbol := range got[0] {
		legal := symbol == '_' || symbol == '-' ||
			(symbol >= 'a' && symbol <= 'z') ||
			(symbol >= 'A' && symbol <= 'Z') ||
			(symbol >= '0' && symbol <= '9')
		if !legal {
			t.Fatalf("name %q carries the illegal character %q", got[0], symbol)
		}
	}
}

// TestCollidingToolNamesAreMadeUnique is the rule that keeps a duplicate from
// silently disappearing: the registry indexes by name, so the second scan would
// otherwise be unreachable.
func TestCollidingToolNamesAreMadeUnique(t *testing.T) {
	pool := connectFake(t, modeCollision)

	got := names(pool.Tools())
	if len(got) != 2 {
		t.Fatalf("tools = %v, want both entries kept", got)
	}
	if got[0] == got[1] {
		t.Fatalf("both tools are named %q, so one is unreachable", got[0])
	}
}

// TestLongNamesAreClippedToTheProviderLimit keeps a long tool name from being
// sent and rejected, which would fail the whole request rather than one call.
func TestLongNamesAreClippedToTheProviderLimit(t *testing.T) {
	cases := []struct{ server, tool string }{
		{"a-very-long-server-name-that-goes-on-and-on-for-ages", "scan"},
		{"short", strings.Repeat("t", 200)},
		{"both-halves-are-quite-long-indeed-here", strings.Repeat("u", 120)},
	}
	for _, testCase := range cases {
		got := qualify(testCase.server, testCase.tool)
		if len(got) > maxToolNameLength {
			t.Errorf("qualify(%q, %q) = %d characters, over the limit", testCase.server, testCase.tool, len(got))
		}
		if !strings.HasPrefix(got, ReservedPrefix) {
			t.Errorf("qualify(%q, %q) = %q, want the reserved prefix kept", testCase.server, testCase.tool, got)
		}
	}
}

// TestEmptyNamesStillProduceALegalTool is the degenerate case: a server that
// reports a name made only of illegal characters must still yield something the
// provider accepts.
func TestEmptyNamesStillProduceALegalTool(t *testing.T) {
	got := qualify("///", "***")
	if got != "mcp_server__tool" {
		t.Errorf("qualify = %q, want the fallbacks", got)
	}
}

// TestCallRoutesToTheOwningServer walks the dispatch path.
func TestCallRoutesToTheOwningServer(t *testing.T) {
	pool := connectFake(t, modeNormal)

	result, err := pool.Call(context.Background(), "mcp_fake__echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if result.Text() != "echo: hi" {
		t.Errorf("Text = %q, want the echoed text", result.Text())
	}
}

// TestCallRejectsAnUnknownTool keeps a name that is not contributed from
// reaching a server that never published it.
func TestCallRejectsAnUnknownTool(t *testing.T) {
	pool := connectFake(t, modeNormal)

	if _, err := pool.Call(context.Background(), "mcp_fake__nope", nil); err == nil {
		t.Fatalf("an unknown contributed tool must be refused")
	}
	if _, err := pool.Call(context.Background(), "write_file", nil); err == nil {
		t.Fatalf("a built-in name must not resolve through the pool")
	}
}

// TestABrokenServerDoesNotStopTheOthers is the reason Connect is best effort:
// an operator with several servers must not lose the working ones to a typo.
func TestABrokenServerDoesNotStopTheOthers(t *testing.T) {
	pool := NewPool()
	t.Cleanup(pool.Close)
	broken := fakeOptions(modeCrash)
	broken.Name = "broken"
	pool.Connect(t.Context(), nil, []Options{broken, fakeOptions(modeNormal)})

	if got := len(pool.Tools()); got != 2 {
		t.Errorf("tools = %d, want the healthy server's two", got)
	}
	failures := pool.Failures()
	if len(failures) != 1 || failures[0].Name != "broken" {
		t.Fatalf("failures = %+v, want only the broken server", failures)
	}
	if !strings.Contains(failures[0].Err.Error(), "cannot find module") {
		t.Errorf("the failure should carry the server's own message, got %v", failures[0].Err)
	}
}

// TestStatusReportsEveryConfiguredServer is the operator's view: a server that
// is off, one that is connected and one that failed all have to be visible, or
// a config typo looks like nothing at all.
func TestStatusReportsEveryConfiguredServer(t *testing.T) {
	pool := NewPool()
	t.Cleanup(pool.Close)

	off := fakeOptions(modeNormal)
	off.Name = "off"
	broken := fakeOptions(modeCrash)
	broken.Name = "broken"
	pool.Connect(t.Context(), []Options{off}, []Options{broken, fakeOptions(modeNormal)})

	status := pool.Status()
	if len(status) != 3 {
		t.Fatalf("status has %d entries, want 3", len(status))
	}
	byName := map[string]ServerStatus{}
	for _, entry := range status {
		byName[entry.Name] = entry
	}
	if !byName["off"].Disabled {
		t.Errorf("a disabled server must be reported as disabled")
	}
	if byName["off"].Connected {
		t.Errorf("a disabled server must not be reported as connected")
	}
	if byName["broken"].Err == nil {
		t.Errorf("a failed server must carry its error")
	}
	healthy := byName["fake"]
	if !healthy.Connected || healthy.ToolCount != 2 {
		t.Errorf("healthy server = %+v, want connected with two tools", healthy)
	}
	if healthy.ServerName != "fake-server" {
		t.Errorf("ServerName = %q, want the name the server reported", healthy.ServerName)
	}
	if healthy.Command != fakeOptions(modeNormal).Command+" -test.run=^$" {
		t.Errorf("Command = %q, want the reproducible command line", healthy.Command)
	}
}

// TestCloseEmptiesThePool keeps a shutdown from leaving a tool the model can
// still see but no longer reach.
func TestCloseEmptiesThePool(t *testing.T) {
	pool := connectFake(t, modeNormal)
	if len(pool.Tools()) == 0 {
		t.Fatalf("the fixture contributed nothing")
	}

	pool.Close()
	if got := len(pool.Tools()); got != 0 {
		t.Errorf("tools = %d after Close, want none", got)
	}
	if _, err := pool.Call(context.Background(), "mcp_fake__echo", nil); err == nil {
		t.Errorf("a closed pool must refuse a call")
	}
}

// TestSanitize pins the character rule on its own, including the run collapsing
// that keeps two spellings of a name from producing two tools.
func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"plain":         "plain",
		"with space":    "with_space",
		"two  spaces":   "two_spaces",
		"a/b":           "a_b",
		"keep-_these":   "keep-_these",
		"trailing___":   "trailing",
		"__leading":     "leading",
		"dots.and(are)": "dots_and_are",
		"emoji-ok":      "emoji-ok",
		"":              "",
		"***":           "",
	}
	for input, want := range cases {
		if got := sanitize(input); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", input, got, want)
		}
	}
}
