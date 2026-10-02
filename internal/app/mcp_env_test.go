package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

// TestMCPEnvironmentMovesToTheSecretFile pins the migration: a token an older
// version left in config.json ends up in the 0600 secret file and is removed
// from the config.
func TestMCPEnvironmentMovesToTheSecretFile(t *testing.T) {
	application := newTestApp(t)
	if err := application.UpdateConfig(func(cfg *config.Config) {
		cfg.MCPServers = []config.MCPServer{{
			Name:    "github",
			Command: "mcp-server-github",
			Env:     map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "secret-token"},
		}}
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	application.migrateMCPEnv()

	cfg := application.Config()
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("servers = %+v", cfg.MCPServers)
	}
	if len(cfg.MCPServers[0].Env) != 0 {
		t.Errorf("config still carries the MCP env: %+v", cfg.MCPServers[0].Env)
	}
	if got := application.mcpEnvFor("github")["GITHUB_PERSONAL_ACCESS_TOKEN"]; got != "secret-token" {
		t.Errorf("secret env = %q, want secret-token", got)
	}
}

// TestSaveMCPEnvRoundTrips proves a fresh environment can be stored and read
// back, which is what the loader uses to launch the server.
func TestSaveMCPEnvRoundTrips(t *testing.T) {
	application := newTestApp(t)
	if err := application.saveMCPEnv("memory", map[string]string{"MEMORY_FILE": "/tmp/memory.json"}); err != nil {
		t.Fatalf("saveMCPEnv: %v", err)
	}
	if got := application.mcpEnvFor("memory")["MEMORY_FILE"]; got != "/tmp/memory.json" {
		t.Errorf("MEMORY_FILE = %q", got)
	}
}
