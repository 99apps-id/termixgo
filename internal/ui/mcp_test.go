package ui

import (
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

// runMCP sends the /mcp command and returns the resulting model.
func runMCP(t *testing.T, model *Model, args string) *Model {
	t.Helper()
	next, _ := model.runSlash("mcp", args)
	updated, ok := next.(*Model)
	if !ok {
		t.Fatalf("runSlash returned %T, want *Model", next)
	}
	return updated
}

// setServers writes the MCP servers into the config and reloads, which is what
// an operator does after editing the settings file. The pool only reflects what
// was live at startup, so a change without a reload is not meant to show up.
func setServers(t *testing.T, model *Model, servers ...config.MCPServer) *Model {
	t.Helper()
	if err := model.app.UpdateConfig(func(cfg *config.Config) {
		cfg.MCPServers = servers
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	return runMCP(t, model, "reload")
}

// TestSlashMCPWithoutServersExplainsHowToAddOne is the discovery path: an
// operator who types /mcp on a fresh install has to learn what to write and
// where, rather than being told there is nothing.
func TestSlashMCPWithoutServersExplainsHowToAddOne(t *testing.T) {
	kind, text := newestBlock(t, runMCP(t, chatModel(t), ""))

	if kind != blockNotice {
		t.Fatalf("kind = %d, want a notice", kind)
	}
	for _, want := range []string{"No MCP servers", "mcpServers", "reload"} {
		if !strings.Contains(text, want) {
			t.Errorf("the guidance should mention %q, got:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "command") {
		t.Errorf("the example should show the required field, got:\n%s", text)
	}
}

// TestSlashMCPListsAServerAndItsCommand is what an operator reads to check a
// config: the name, whether it is up, and its command so a failure can be
// reproduced by hand.
func TestSlashMCPListsAServerAndItsCommand(t *testing.T) {
	model := setServers(t, chatModel(t), config.MCPServer{
		Name:    "files",
		Command: "a-command-that-does-not-exist-anywhere",
	})

	kind, text := newestBlock(t, model)
	if kind != blockNotice {
		t.Fatalf("kind = %d, want a notice", kind)
	}
	if !strings.Contains(text, "files") {
		t.Errorf("the server name is missing:\n%s", text)
	}
	if !strings.Contains(text, "a-command-that-does-not-exist-anywhere") {
		t.Errorf("the command is missing, so the operator cannot reproduce the failure:\n%s", text)
	}
	if !strings.Contains(text, "failed") {
		t.Errorf("a server that could not start should read as failed:\n%s", text)
	}
}

// TestSlashMCPShowsADisabledServer keeps a parked server visible, so an
// operator does not read its absence as a config typo.
func TestSlashMCPShowsADisabledServer(t *testing.T) {
	model := setServers(t, chatModel(t), config.MCPServer{
		Name:     "parked",
		Command:  "npx",
		Disabled: true,
	})

	_, text := newestBlock(t, model)
	if !strings.Contains(text, "parked") {
		t.Errorf("a disabled server must still be listed:\n%s", text)
	}
	if !strings.Contains(text, "off") {
		t.Errorf("a disabled server should read as off:\n%s", text)
	}
}

// TestSlashMCPRejectsAnUnknownArgument keeps a typo from silently doing
// nothing, which would look like the command worked.
func TestSlashMCPRejectsAnUnknownArgument(t *testing.T) {
	kind, text := newestBlock(t, runMCP(t, chatModel(t), "restart"))

	if kind != blockError {
		t.Fatalf("kind = %d, want an error", kind)
	}
	if !strings.Contains(text, "Usage") {
		t.Errorf("the error should show the usage, got %q", text)
	}
}

// TestSlashMCPReloadIsSafeWithNothingConfigured covers the operator reloading
// before setting anything up.
func TestSlashMCPReloadIsSafeWithNothingConfigured(t *testing.T) {
	kind, text := newestBlock(t, runMCP(t, chatModel(t), "reload"))

	if kind != blockNotice {
		t.Fatalf("kind = %d, want a notice", kind)
	}
	if strings.TrimSpace(text) == "" {
		t.Errorf("a reload with nothing configured must still say something")
	}
}

// TestMCPIsInTheSlashList keeps the command discoverable from /help.
func TestMCPIsInTheSlashList(t *testing.T) {
	found := false
	for _, command := range SlashCommands() {
		if command.Trigger == "/mcp" {
			found = true
			if !strings.Contains(command.Summary, "MCP") {
				t.Errorf("the summary should name MCP, got %q", command.Summary)
			}
		}
	}
	if !found {
		t.Errorf("/mcp is missing from the command list")
	}
}
