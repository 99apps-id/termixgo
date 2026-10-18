package main

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

// TestMCPCommandWithNothingConfiguredGuidesTheOperator is the first experience:
// the command has to say where to write a server rather than printing nothing.
func TestMCPCommandWithNothingConfiguredGuidesTheOperator(t *testing.T) {
	withState(t)

	stdout, _, err := runCLI(t, "mcp")
	if err != nil {
		t.Fatalf("mcp: %v", err)
	}
	for _, want := range []string{"No MCP servers", "mcpServers"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output should mention %q, got %q", want, stdout)
		}
	}
}

// TestMCPCommandReportsAFailingServer is the reason the command connects for
// real instead of only reading the config: a wrong command or a missing package
// is the failure that matters, and only starting the server shows it.
func TestMCPCommandReportsAFailingServer(t *testing.T) {
	withState(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.MCPServers = []config.MCPServer{{
		Name:    "broken",
		Command: "a-command-that-does-not-exist-anywhere",
	}}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stdout, _, err := runCLI(t, "mcp")
	if err == nil {
		t.Fatalf("a broken server must produce a non-zero exit so a script can see it")
	}
	if !strings.Contains(stdout, "broken") {
		t.Errorf("the server name is missing: %q", stdout)
	}
	if !strings.Contains(stdout, "failed") {
		t.Errorf("the failure should be named: %q", stdout)
	}
	if !strings.Contains(stdout, "a-command-that-does-not-exist-anywhere") {
		t.Errorf("the command should be shown so it can be fixed by hand: %q", stdout)
	}
}

// TestMCPCommandReportsADisabledServer keeps a parked server visible.
func TestMCPCommandReportsADisabledServer(t *testing.T) {
	withState(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.MCPServers = []config.MCPServer{{Name: "parked", Command: "npx", Disabled: true}}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stdout, _, err := runCLI(t, "mcp")
	if err != nil {
		t.Fatalf("a disabled server is not a failure: %v", err)
	}
	if !strings.Contains(stdout, "parked") || !strings.Contains(stdout, "off") {
		t.Errorf("a disabled server should be listed as off, got %q", stdout)
	}
}

// TestMCPCommandRejectsAnUnknownArgument keeps a typo from being ignored, which
// would look like the command worked.
func TestMCPCommandRejectsAnUnknownArgument(t *testing.T) {
	withState(t)

	_, _, err := runCLI(t, "mcp", "restart")
	if err == nil {
		t.Fatalf("an unknown subcommand must be rejected")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Errorf("the error should show the usage, got %v", err)
	}
}

// TestMCPCommandAcceptsListExplicitly covers the documented spelling.
func TestMCPCommandAcceptsListExplicitly(t *testing.T) {
	withState(t)

	if _, _, err := runCLI(t, "mcp", "list"); err != nil {
		t.Fatalf("mcp list: %v", err)
	}
}

// TestHelpMentionsMCP keeps the command discoverable from the CLI help.
func TestHelpMentionsMCP(t *testing.T) {
	withState(t)

	stdout, _, err := runCLI(t, "help")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(stdout, "termixgo mcp") {
		t.Errorf("help should list the mcp command:\n%s", stdout)
	}
}
