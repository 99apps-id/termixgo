// Package mcp speaks the Model Context Protocol over stdio, so a Termixgo
// session can use the tools an MCP server publishes.
//
// Only the stdio transport is implemented, and only the three calls the agent
// needs: initialize, tools/list and tools/call. Those are the stable core of
// the protocol. Resources, prompts and sampling are deliberately out of scope,
// because the agent already has its own answer for each of them.
package mcp

import (
	"encoding/json"
	"strconv"
)

// ProtocolVersion is the revision of the protocol this client speaks. The
// server answers with the revision it will use, and that answer is recorded
// rather than enforced: the three calls above have been compatible across every
// revision so far, so refusing a mismatch would break working servers for no
// gain.
const ProtocolVersion = "2025-06-18"

// ClientName and ClientVersion identify this client to the server during the
// handshake.
const (
	ClientName    = "termixgo"
	ClientVersion = "1.0"
)

// Tool is one tool an MCP server publishes, in the shape tools/list returns.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	Annotations *Annotations   `json:"annotations,omitempty"`
}

// Annotations carries the server's own hints about a tool.
//
// They come from an untrusted process, so they are allowed to make a tool look
// safer than the default and never less safe: a server that omits them gets the
// cautious treatment of a tool that can change things.
type Annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  bool   `json:"idempotentHint,omitempty"`
	OpenWorldHint   bool   `json:"openWorldHint,omitempty"`
}

// Content is one block of a tool result.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Data and MimeType carry a non-text block. It is reported by type rather
	// than inlined, because the model reads text and a base64 image would
	// crowd out the answer.
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// CallResult is what tools/call returns.
type CallResult struct {
	Content []Content `json:"content,omitempty"`
	IsError bool      `json:"isError,omitempty"`
}

// Text renders a result as the text the model should read. A non-text block is
// described rather than dropped, so a tool that returns only an image does not
// look like a tool that returned nothing.
func (r CallResult) Text() string {
	lines := make([]string, 0, len(r.Content))
	for _, block := range r.Content {
		switch {
		case block.Type == "text" || block.Type == "":
			lines = append(lines, block.Text)
		case block.MimeType != "":
			lines = append(lines, "[", block.Type, " block: ", block.MimeType, "]")
		default:
			lines = append(lines, "[", block.Type, " block]")
		}
	}
	out := ""
	for _, line := range lines {
		out += line
	}
	if out == "" {
		// An empty result is legal and means the tool did its job silently.
		return "(no output)"
	}
	return out
}

// InitializeResult is the handshake answer.
type InitializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

// ListToolsResult is the tools/list answer.
type ListToolsResult struct {
	Tools []Tool `json:"tools"`
	// NextCursor is set when the server paginates its tool list.
	NextCursor string `json:"nextCursor,omitempty"`
}

// request is one JSON-RPC call. ID is omitted for a notification, which is how
// notifications/initialized is sent.
type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// response is one JSON-RPC message read from the server. A message may carry a
// result, an error, or neither when it is a notification the client can ignore.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the error object of a failed call.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return "the server returned no result"
	}
	if e.Message == "" {
		return "the server returned error " + strconv.Itoa(e.Code)
	}
	return e.Message
}
