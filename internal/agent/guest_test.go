package agent

import (
	"strings"
	"testing"
)

func TestRegistryWithout(t *testing.T) {
	registry := DefaultRegistry()
	trimmed := registry.Without("run_command", "process_list")
	if _, ok := trimmed.Lookup("run_command"); ok {
		t.Errorf("a denied tool must be gone")
	}
	if _, ok := trimmed.Lookup("read_file"); !ok {
		t.Errorf("the other tools must stay")
	}
	if _, ok := registry.Lookup("run_command"); !ok {
		t.Errorf("Without must not mutate the receiver")
	}
	// A name that no tool carries is ignored, so a stale config entry costs
	// nothing.
	_ = registry.Without("no_such_tool")
}

func TestRegistryWithoutPrefix(t *testing.T) {
	registry := NewRegistry(
		NewMCPTool(MCPToolSpec{Name: "mcp_github__search", RemoteName: "search", Description: "Search."}),
		&readFileTool{},
	)
	trimmed := registry.WithoutPrefix("mcp_")
	if _, ok := trimmed.Lookup("mcp_github__search"); ok {
		t.Errorf("an MCP tool must be gone for a guest")
	}
	if _, ok := trimmed.Lookup("read_file"); !ok {
		t.Errorf("a built-in tool must stay")
	}
}

func TestGuestPromptBlock(t *testing.T) {
	static, _ := BuildSystemParts(&Env{Workspace: t.TempDir(), Guest: true}, "test-model")
	if !strings.Contains(static, "## GUEST SESSION") {
		t.Fatalf("a guest run must carry the guest block")
	}
	if !strings.Contains(static, "Maaf, itu hanya bisa dilakukan operator.") {
		t.Errorf("the refusal example is missing")
	}
	admin, _ := BuildSystemParts(&Env{Workspace: t.TempDir()}, "test-model")
	if strings.Contains(admin, "## GUEST SESSION") {
		t.Errorf("an operator run must not carry the guest block")
	}
}

func TestEvidenceBlockAndStrictLine(t *testing.T) {
	static, _ := BuildSystemParts(&Env{Workspace: t.TempDir()}, "test-model")
	for _, want := range []string{"## EVIDENCE", "path and line", "URL", "saya tidak menemukannya"} {
		if !strings.Contains(static, want) {
			t.Errorf("the evidence block is missing %q", want)
		}
	}
	if strings.Contains(static, "Strict grounding is on") {
		t.Errorf("the strict line must not appear for a normal run")
	}
	strict, _ := BuildSystemParts(&Env{Workspace: t.TempDir(), Grounding: "strict"}, "test-model")
	if !strings.Contains(strict, "Strict grounding is on") {
		t.Errorf("a strict run must say so")
	}
}

func TestDelegationDepthExceeded(t *testing.T) {
	if err := delegationDepthExceeded(&Env{Depth: 0}); err != nil {
		t.Errorf("no configured depth is no cap: %v", err)
	}
	if err := delegationDepthExceeded(&Env{Depth: 1, DelegationMaxDepth: 2}); err != nil {
		t.Errorf("depth 2 is allowed at maxDepth 2: %v", err)
	}
	if err := delegationDepthExceeded(&Env{Depth: 2, DelegationMaxDepth: 2}); err == nil {
		t.Errorf("depth 3 must be refused at maxDepth 2")
	}
}
