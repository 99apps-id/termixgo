package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

// TestMCPHeadersMoveToTheSecretStore pins the rule that an MCP bearer token is
// never left in config.json, which has no explicit owner-only ACL on Windows.
func TestMCPHeadersMoveToTheSecretStore(t *testing.T) {
	application := newTestApp(t)
	if err := application.UpdateConfig(func(c *config.Config) {
		c.MCPServers = []config.MCPServer{{
			Name:    "remote",
			URL:     "https://example.com/mcp",
			Headers: map[string]string{"Authorization": "Bearer token"},
		}}
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	application.migrateMCPEnv()

	headers := application.mcpHeadersFor("remote")
	if headers["Authorization"] != "Bearer token" {
		t.Fatalf("headers = %v", headers)
	}
	cfg := application.Config()
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("servers = %+v", cfg.MCPServers)
	}
	if len(cfg.MCPServers[0].Headers) != 0 {
		t.Fatalf("the header stayed in config.json: %v", cfg.MCPServers[0].Headers)
	}
}
