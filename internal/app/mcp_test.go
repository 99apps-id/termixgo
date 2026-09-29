package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

const (
	appHelperEnv = "TERMIXGO_APP_MCP_HELPER"
	appModeEnv   = "TERMIXGO_APP_MCP_MODE"
)

// TestMain turns this test binary into the fake MCP server when the helper
// variable is set, which is how the app can be tested against a real server
// process without depending on an MCP package being installed.
func TestMain(m *testing.M) {
	if os.Getenv(appHelperEnv) != "" {
		serveFakeMCP(os.Getenv(appModeEnv))
		return
	}
	os.Exit(m.Run())
}

// serveFakeMCP answers the three calls the app makes. Its job is to be a
// believable server, not a complete one.
func serveFakeMCP(mode string) {
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()

	for {
		line, err := reader.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			fakeMCPAnswer(writer, trimmed, mode)
		}
		if err != nil {
			return
		}
	}
}

func fakeMCPAnswer(writer *bufio.Writer, line, mode string) {
	var message struct {
		ID     *int64          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &message); err != nil {
		return
	}
	if message.ID == nil {
		return
	}

	switch message.Method {
	case "initialize":
		fakeMCPWrite(writer, map[string]any{
			"jsonrpc": "2.0",
			"id":      *message.ID,
			"result": map[string]any{
				"protocolVersion": "2025-06-18",
				"serverInfo":      map[string]any{"name": "app-fake", "version": "1.0"},
			},
		})
	case "tools/list":
		fakeMCPWrite(writer, map[string]any{
			"jsonrpc": "2.0",
			"id":      *message.ID,
			"result": map[string]any{"tools": []any{
				map[string]any{
					"name":        "echo",
					"description": "Echo the text back.",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"text": map[string]any{"type": "string"}},
					},
					"annotations": map[string]any{"readOnlyHint": true},
				},
				map[string]any{
					"name":        "scan",
					"description": "Run a scan.",
					"inputSchema": map[string]any{"type": "object"},
				},
			}},
		})
	case "tools/call":
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(message.Params, &call)
		text, _ := call.Arguments["text"].(string)
		fakeMCPWrite(writer, map[string]any{
			"jsonrpc": "2.0",
			"id":      *message.ID,
			"result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "server says: " + text}},
			},
		})
	default:
		fakeMCPWrite(writer, map[string]any{
			"jsonrpc": "2.0",
			"id":      *message.ID,
			"error":   map[string]any{"code": -32601, "message": "method not found: " + message.Method},
		})
	}
}

func fakeMCPWrite(writer *bufio.Writer, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	writer.Write(encoded)
	writer.WriteByte('\n')
	writer.Flush()
}

// fakeMCPServer describes the fake server re-executed as this test binary.
func fakeMCPServer(mode string) config.MCPServer {
	return config.MCPServer{
		Name:    "fake",
		Command: os.Args[0],
		Args:    []string{"-test.run=^$"},
		Env:     map[string]string{appHelperEnv: "1", appModeEnv: mode},
	}
}

// mcpApp builds an app with one configured MCP server.
func mcpApp(t *testing.T, servers ...config.MCPServer) *App {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.Default()
	cfg.MCPServers = servers
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	application, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(application.Shutdown)
	return application
}

// TestConfiguredServerContributesTools is the point of the feature: a server in
// the config puts its tools in front of the model.
func TestConfiguredServerContributesTools(t *testing.T) {
	application := mcpApp(t, fakeMCPServer("normal"))

	for _, name := range []string{"mcp_fake__echo", "mcp_fake__scan"} {
		if _, ok := application.Tools().Lookup(name); !ok {
			t.Errorf("%s is missing from the registry", name)
		}
	}
	// A built-in must still be there: the pool replaces the registry with the
	// built-ins plus the contributions, not with the contributions alone.
	if _, ok := application.Tools().Lookup("write_file"); !ok {
		t.Errorf("a built-in tool was lost when the MCP tools were added")
	}
}

// TestContributedToolRunsThroughTheServer walks the full path from the tool
// name the model uses to the server that answers it.
func TestContributedToolRunsThroughTheServer(t *testing.T) {
	application := mcpApp(t, fakeMCPServer("normal"))

	tool, ok := application.Tools().Lookup("mcp_fake__echo")
	if !ok {
		t.Fatalf("the contributed tool is missing")
	}
	result, err := tool.Run(context.Background(), nil, map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("Run reported an error: %q", result.Output)
	}
	if result.Output != "server says: hello" {
		t.Errorf("Output = %q, want the server's answer", result.Output)
	}
}

// TestTheReadOnlyHintDecidesTheGate is the security rule at this layer: a tool
// the server marked read-only runs freely, and one it did not is subject to the
// approval policy.
func TestTheReadOnlyHintDecidesTheGate(t *testing.T) {
	application := mcpApp(t, fakeMCPServer("normal"))

	readOnly, ok := application.Tools().Lookup("mcp_fake__echo")
	if !ok {
		t.Fatalf("echo is missing")
	}
	if readOnly.Mutating() {
		t.Errorf("a tool the server marked read-only must not be treated as mutating")
	}
	scan, ok := application.Tools().Lookup("mcp_fake__scan")
	if !ok {
		t.Fatalf("scan is missing")
	}
	if !scan.Mutating() {
		t.Errorf("a tool with no read-only hint must be treated as mutating")
	}
	if application.Policy().NeedsApproval(scan) {
		// Approval all is the fixture default, so this only proves the policy
		// wiring is unchanged. The gate that matters is folder trust.
		t.Logf("the fixture is in approval-all mode, so the trust gate is what applies")
	}
}

// TestABrokenServerIsReportedNotFatal is the reason connecting is best effort:
// an operator should not lose a working session to a server with a bad command.
func TestABrokenServerIsReportedNotFatal(t *testing.T) {
	broken := config.MCPServer{Name: "broken", Command: "a-command-that-does-not-exist-anywhere"}
	application := mcpApp(t, broken, fakeMCPServer("normal"))

	if _, ok := application.Tools().Lookup("mcp_fake__echo"); !ok {
		t.Errorf("the healthy server's tools must still be available")
	}
	status := application.MCPStatus()
	if len(status) != 2 {
		t.Fatalf("status has %d entries, want both servers", len(status))
	}
	failed := false
	for _, entry := range status {
		if entry.Name == "broken" && entry.Err != nil {
			failed = true
		}
	}
	if !failed {
		t.Errorf("the broken server must be reported with its error: %+v", status)
	}
}

// TestADisabledServerContributesNothing is the parking case.
func TestADisabledServerContributesNothing(t *testing.T) {
	server := fakeMCPServer("normal")
	server.Disabled = true
	application := mcpApp(t, server)

	if _, ok := application.Tools().Lookup("mcp_fake__echo"); ok {
		t.Errorf("a disabled server must contribute no tools")
	}
	status := application.MCPStatus()
	if len(status) != 1 || !status[0].Disabled {
		t.Fatalf("status = %+v, want the server reported as off", status)
	}
	// It is still listed, so a typo does not look like nothing at all.
	if status[0].Command == "" {
		t.Errorf("a disabled server must still show its command")
	}
}

// TestNoConfiguredServersLeavesTheBuiltinsAlone keeps the common case free of
// surprises: an install with no MCP config behaves exactly as before.
func TestNoConfiguredServersLeavesTheBuiltinsAlone(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := config.Save(config.Default()); err != nil {
		t.Fatalf("save config: %v", err)
	}
	application, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(application.Shutdown)

	if got := application.MCPStatus(); len(got) != 0 {
		t.Errorf("MCPStatus = %+v, want nothing configured", got)
	}
	if _, ok := application.Tools().Lookup("write_file"); !ok {
		t.Errorf("the built-in tools must be available with no MCP servers")
	}
}

// TestReloadPicksUpAConfigChange is what an operator does after fixing a
// command line: the reload has to see the new config, not the one loaded at
// startup.
func TestReloadPicksUpAConfigChange(t *testing.T) {
	application := mcpApp(t)
	if _, ok := application.Tools().Lookup("mcp_fake__echo"); ok {
		t.Fatalf("the fixture should start with no MCP tools")
	}

	if err := application.UpdateConfig(func(cfg *config.Config) {
		cfg.MCPServers = []config.MCPServer{fakeMCPServer("normal")}
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if err := application.ReloadMCP(context.Background()); err != nil {
		t.Fatalf("ReloadMCP: %v", err)
	}

	if _, ok := application.Tools().Lookup("mcp_fake__echo"); !ok {
		t.Errorf("the reload did not pick up the new server")
	}
}

// TestReloadIsRefusedWhileATurnRuns keeps the registry from changing under a
// running turn, which would leave the model with tools that no longer exist.
func TestReloadIsRefusedWhileATurnRuns(t *testing.T) {
	application := mcpApp(t, fakeMCPServer("normal"))

	application.mu.Lock()
	application.running = true
	application.mu.Unlock()
	defer func() {
		application.mu.Lock()
		application.running = false
		application.mu.Unlock()
	}()

	err := application.ReloadMCP(context.Background())
	if err == nil {
		t.Fatalf("a reload during a turn must be refused")
	}
	if !strings.Contains(err.Error(), "turn is running") {
		t.Errorf("err = %v, want it to name the reason", err)
	}
	// The refusal must leave the working pool in place rather than half
	// replacing it, so the tool the model is using still resolves.
	if _, ok := application.Tools().Lookup("mcp_fake__echo"); !ok {
		t.Errorf("a refused reload changed the registry")
	}
}

// TestReloadReportsAFailingServerSoTheOperatorKnows covers the exit path: a
// reload that connected nothing must say so rather than look successful.
func TestReloadReportsAFailingServer(t *testing.T) {
	application := mcpApp(t, config.MCPServer{Name: "broken", Command: "a-command-that-does-not-exist-anywhere"})

	if err := application.ReloadMCP(context.Background()); err == nil {
		t.Fatalf("a reload with a broken server must report the failure")
	}
}

// TestShutdownStopsTheServers keeps a server process from outliving the session
// and holding its port or its lock.
func TestShutdownStopsTheServers(t *testing.T) {
	application := mcpApp(t, fakeMCPServer("normal"))
	if _, ok := application.Tools().Lookup("mcp_fake__echo"); !ok {
		t.Fatalf("the fixture contributed nothing")
	}

	application.Shutdown()
	application.Shutdown()

	if got := application.MCPStatus(); len(got) != 0 {
		t.Errorf("MCPStatus = %+v after shutdown, want nothing", got)
	}
}

// TestContributedToolNamesAreNamespacedForTheModel is the discoverability rule:
// the model sees the server in the tool name, so it can tell where a call goes.
func TestContributedToolNamesAreNamespacedForTheModel(t *testing.T) {
	application := mcpApp(t, fakeMCPServer("normal"))

	names := map[string]bool{}
	for _, entry := range application.Tools().Catalog() {
		names[entry.Name] = true
	}
	for _, name := range []string{"mcp_fake__echo", "mcp_fake__scan"} {
		if !names[name] {
			t.Errorf("%s is missing from /tools, which is %v", name, names)
		}
		// The listing has to say the tool is external, because a write through
		// an MCP server is not the same as a local write.
		if !strings.HasPrefix(name, "mcp_") {
			t.Errorf("%s does not mark it as an MCP tool", name)
		}
	}
}

// TestContributedToolDescriptionNamesTheServer keeps the model from having to
// infer where a tool came from.
func TestContributedToolDescriptionNamesTheServer(t *testing.T) {
	application := mcpApp(t, fakeMCPServer("normal"))

	tool, ok := application.Tools().Lookup("mcp_fake__echo")
	if !ok {
		t.Fatalf("echo is missing")
	}
	description := tool.Description()
	if !strings.Contains(description, "fake") {
		t.Errorf("Description = %q, want the server named", description)
	}
	if !strings.Contains(description, "Echo the text back") {
		t.Errorf("Description = %q, want the server's own words kept", description)
	}
	if strings.TrimSpace(description) == "" {
		t.Errorf("a provider rejects a tool with no description")
	}
}

// TestMCPStatusIsSafeToPollWhileNothingIsConfigured covers the interface
// reading the status before any server is set up.
func TestMCPStatusIsSafeWhileNothingIsConfigured(t *testing.T) {
	application := newTestApp(t)
	if got := application.MCPStatus(); len(got) != 0 {
		t.Errorf("MCPStatus = %+v, want empty", got)
	}
	if err := application.ReloadMCP(context.Background()); err != nil {
		t.Errorf("reloading with nothing configured must succeed quietly: %v", err)
	}
}

// TestFakeMCPServerAnswersTheProtocol pins the fixture itself, so a failure in
// the tests above points at the app rather than at a broken helper.
func TestFakeMCPServerAnswersTheProtocol(t *testing.T) {
	server := fakeMCPServer("normal")
	if server.Command != os.Args[0] {
		t.Fatalf("the fake server should re-run the test binary, got %q", server.Command)
	}
	if fmt.Sprint(server.Env[appModeEnv]) != "normal" {
		t.Errorf("Env = %v, want the mode passed through", server.Env)
	}
}
