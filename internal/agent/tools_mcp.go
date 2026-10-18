package agent

import (
	"context"
	"fmt"
	"strings"
)

// MCPToolSpec is everything the agent needs to expose one tool from an MCP
// server.
//
// The caller supplies Call, which is what keeps this package free of the
// protocol dependency: the agent knows how to present and gate an external
// tool, and the mcp package knows how to speak to the server, and neither has
// to import the other.
type MCPToolSpec struct {
	// Server is the label of the server the tool came from.
	Server string
	// Name is the qualified, provider-safe name the model will use.
	Name string
	// RemoteName is the name the server itself knows the tool by.
	RemoteName string
	// Description is what the server says the tool does.
	Description string
	// Schema is the input schema the server published, offered unchanged so the
	// model sees the arguments the server actually expects.
	Schema map[string]any
	// ReadOnly is the server's own hint. It can only ever lower a tool's risk,
	// never raise it: a server that claims nothing gets the cautious treatment,
	// because annotations come from a process we do not control.
	ReadOnly bool
	// Call runs the tool. The boolean reports a failure the server described in
	// its result, as opposed to an error reaching the server at all.
	Call func(ctx context.Context, args map[string]any) (string, bool, error)
}

// mcpTool presents one MCP server tool as an agent tool.
type mcpTool struct {
	spec MCPToolSpec
}

// NewMCPTool wraps a contributed tool for the registry.
func NewMCPTool(spec MCPToolSpec) Tool { return &mcpTool{spec: spec} }

func (t *mcpTool) Name() string { return t.spec.Name }

// Aliases is empty on purpose. An alias would be a second name the model could
// reach the same external tool by, which makes the transcript harder to read
// and gives a collision two chances to happen.
func (t *mcpTool) Aliases() []string { return nil }

func (t *mcpTool) Description() string {
	description := strings.TrimSpace(t.spec.Description)
	if description == "" {
		// A provider rejects a tool with no description, so a server that omits
		// one still gets a usable line rather than breaking the whole request.
		description = fmt.Sprintf("Runs %s on the %s MCP server.", t.spec.RemoteName, t.spec.Server)
	}
	return fmt.Sprintf("%s (MCP tool from the %s server)", description, t.spec.Server)
}

func (t *mcpTool) Schema() map[string]any {
	schema := t.spec.Schema
	if len(schema) == 0 {
		return object(map[string]any{})
	}
	// A provider requires the arguments to be described as an object. A server
	// that published a schema without the type still gets a usable definition
	// instead of a request the provider refuses.
	if _, ok := schema["type"]; !ok {
		patched := make(map[string]any, len(schema)+1)
		for key, value := range schema {
			patched[key] = value
		}
		patched["type"] = "object"
		return patched
	}
	return schema
}

// Mutating is true unless the server claimed the tool only reads.
//
// This is the important default: the tool runs in another process we did not
// write, so the approval gate and the folder trust check both apply to it.
func (t *mcpTool) Mutating() bool { return !t.spec.ReadOnly }

// Risk classifies the tool as running something, which is what it does: the
// work happens in a separate process whose effects the agent cannot bound.
func (t *mcpTool) Risk() Risk { return RiskCommand }

func (t *mcpTool) Label(args map[string]any) string {
	return "Calling " + t.spec.RemoteName + " on " + t.spec.Server
}

func (t *mcpTool) DoneLabel(args map[string]any) string {
	return "Called " + t.spec.RemoteName
}

// Run calls the server.
//
// A failure the server described is reported as a tool error so the model can
// act on it, and an error reaching the server is reported the same way: from the
// model's point of view both mean the call did not succeed, and the text says
// which one happened.
func (t *mcpTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if t.spec.Call == nil {
		return Result{Output: fmt.Sprintf("The MCP server %s is not connected.", t.spec.Server), IsError: true}, nil
	}
	text, failed, err := t.spec.Call(ctx, args)
	if err != nil {
		return Result{Output: fmt.Sprintf("%s failed: %v", t.spec.Name, err), IsError: true}, nil
	}
	output := strings.TrimSpace(text)
	if output == "" {
		output = "(no output)"
	}
	return Result{Output: output, IsError: failed}, nil
}
