package config

import (
	"strings"
	"testing"
)

// TestMCPServersSurviveARoundTrip is the storage contract: the fields an
// operator hand-edits have to come back exactly as written.
func TestMCPServersSurviveARoundTrip(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	cfg := Default()
	cfg.MCPServers = []MCPServer{{
		Name:    "files",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "."},
		Env:     map[string]string{"TOKEN": "secret-ish"},
	}}

	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.MCPServers) != 1 {
		t.Fatalf("MCPServers = %+v, want one entry", loaded.MCPServers)
	}
	server := loaded.MCPServers[0]
	if server.Name != "files" || server.Command != "npx" {
		t.Errorf("server = %+v, want the name and command kept", server)
	}
	if len(server.Args) != 3 || server.Args[1] != "@modelcontextprotocol/server-filesystem" {
		t.Errorf("Args = %q, want the argument list kept in order", server.Args)
	}
	if server.Env["TOKEN"] != "secret-ish" {
		t.Errorf("Env = %q, want the environment kept", server.Env)
	}
	if server.Disabled {
		t.Errorf("a listed server must default to enabled, since listing it is the intent")
	}
}

// TestAServerWithoutACommandIsDropped keeps a config typo from starting a
// process that cannot exist on every session.
func TestAServerWithoutACommandIsDropped(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	cfg := Default()
	cfg.MCPServers = []MCPServer{
		{Name: "nameless", Command: ""},
		{Name: "", Command: "npx"},
		{Name: "  ", Command: "  "},
		{Name: "good", Command: "npx"},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.MCPServers) != 1 || loaded.MCPServers[0].Name != "good" {
		t.Errorf("MCPServers = %+v, want only the usable one", loaded.MCPServers)
	}
}

// TestDuplicateServerNamesAreDropped is the collision rule at the config edge:
// the name prefixes every tool the server contributes, so two servers sharing
// one would make the second set of tools collide with the first.
func TestDuplicateServerNamesAreDropped(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	cfg := Default()
	cfg.MCPServers = []MCPServer{
		{Name: "files", Command: "first"},
		{Name: "files", Command: "second"},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.MCPServers) != 1 {
		t.Fatalf("MCPServers = %+v, want the duplicate dropped", loaded.MCPServers)
	}
	if loaded.MCPServers[0].Command != "first" {
		t.Errorf("Command = %q, want the first entry kept", loaded.MCPServers[0].Command)
	}
}

// TestServerNamesAreTrimmed keeps a stray space from producing a tool name with
// an underscore in it and a config that reads as two different servers.
func TestServerNamesAreTrimmed(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	cfg := Default()
	cfg.MCPServers = []MCPServer{{Name: "  files  ", Command: "  npx  "}}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	server := loaded.MCPServers[0]
	if server.Name != "files" || server.Command != "npx" {
		t.Errorf("server = %+v, want the whitespace trimmed", server)
	}
	if strings.Contains(server.Name, " ") {
		t.Errorf("Name = %q, want no spaces", server.Name)
	}
}

// TestDisabledServersAreKept covers the parking case: an operator disabling a
// server keeps the command line so they can bring it back.
func TestDisabledServersAreKept(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	cfg := Default()
	cfg.MCPServers = []MCPServer{{Name: "files", Command: "npx", Disabled: true}}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.MCPServers) != 1 || !loaded.MCPServers[0].Disabled {
		t.Errorf("MCPServers = %+v, want the disabled server kept", loaded.MCPServers)
	}
}

func TestWithMCPServerAndMutations(t *testing.T) {
	cfg := Default()

	// Add server
	cfg = cfg.WithMCPServer(MCPServer{Name: "git-mcp", Command: "mcp-git", Args: []string{"--read-only"}})
	if len(cfg.MCPServers) != 1 || cfg.MCPServers[0].Name != "git-mcp" {
		t.Fatalf("expected 1 server, got %+v", cfg.MCPServers)
	}

	// Disable server
	var found bool
	cfg, found = cfg.SetMCPServerDisabled("git-mcp", true)
	if !found || !cfg.MCPServers[0].Disabled {
		t.Errorf("expected server to be disabled, got %+v", cfg.MCPServers[0])
	}

	// Re-enable server
	cfg, found = cfg.SetMCPServerDisabled("git-mcp", false)
	if !found || cfg.MCPServers[0].Disabled {
		t.Errorf("expected server to be enabled, got %+v", cfg.MCPServers[0])
	}

	// Remove server
	cfg, found = cfg.WithoutMCPServer("git-mcp")
	if !found || len(cfg.MCPServers) != 0 {
		t.Errorf("expected server removed, got %+v", cfg.MCPServers)
	}
}
