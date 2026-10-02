package app

import (
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestAppMCPServerMutations(t *testing.T) {
	application := newTestApp(t)

	// Add server
	err := application.AddMCPServer(config.MCPServer{
		Name:    "test-server",
		Command: "npx",
		Args:    []string{"-y", "dummy-server"},
	})
	if err != nil {
		t.Fatalf("AddMCPServer: %v", err)
	}

	cfg := application.Config()
	if len(cfg.MCPServers) != 1 || cfg.MCPServers[0].Name != "test-server" {
		t.Fatalf("expected test-server in config, got %+v", cfg.MCPServers)
	}

	// Disable server
	err = application.SetMCPServerEnabled("test-server", false)
	if err != nil {
		t.Fatalf("SetMCPServerEnabled(false): %v", err)
	}
	if !application.Config().MCPServers[0].Disabled {
		t.Errorf("expected server to be disabled")
	}

	// Enable server
	err = application.SetMCPServerEnabled("test-server", true)
	if err != nil {
		t.Fatalf("SetMCPServerEnabled(true): %v", err)
	}
	if application.Config().MCPServers[0].Disabled {
		t.Errorf("expected server to be enabled")
	}

	// Remove server
	err = application.RemoveMCPServer("test-server")
	if err != nil {
		t.Fatalf("RemoveMCPServer: %v", err)
	}
	if len(application.Config().MCPServers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(application.Config().MCPServers))
	}

	// Remove non-existent
	err = application.RemoveMCPServer("test-server")
	if err == nil {
		t.Errorf("expected error removing non-existent server")
	}
}
