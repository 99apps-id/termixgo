package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// The fake server is the test binary running itself again with an environment
// variable set. That keeps the fixture in the same file as the assertions, so
// it cannot drift from them, and it needs no external MCP package installed.
const (
	helperEnv = "TERMIXGO_MCP_TEST_HELPER"
	modeEnv   = "TERMIXGO_MCP_TEST_MODE"
)

// HelperModes are the behaviours the fake server can be told to act out.
const (
	// modeNormal answers the handshake, lists two tools and echoes a call.
	modeNormal = "normal"
	// modePaginated splits its tool list across two calls.
	modePaginated = "paginated"
	// modeNoTools is a healthy server that publishes nothing.
	modeNoTools = "notools"
	// modeNotification sends a notification before every response.
	modeNotification = "notification"
	// modeProse writes a line that is not JSON to stdout, which poisons the
	// stream and must be reported rather than parsed as a message.
	modeProse = "prose"
	// modeCrash exits at once and explains itself on stderr.
	modeCrash = "crash"
	// modeCallError answers tools/call with a JSON-RPC error.
	modeCallError = "callerror"
	// modeServerError answers tools/call with isError set, which is a tool
	// that ran and reported a failure, not a protocol failure.
	modeServerError = "servererror"
	// modeSilent is a server that completes the handshake and then never
	// answers anything else, which is what a hung server looks like.
	modeSilent = "silent"
	// modeCollision publishes the same tool name twice, which the pool has to
	// make unique.
	modeCollision = "collision"
	// modeUglyToolName publishes a name that is not a legal tool name.
	modeUglyToolName = "uglyname"
)

// runHelperIfRequested runs the fake server when the test binary was
// re-executed with the helper variable set. It returns true when it was the
// helper, so TestMain knows not to run the suite.
func runHelperIfRequested() bool {
	if os.Getenv(helperEnv) == "" {
		return false
	}
	runFakeServer(os.Getenv(modeEnv))
	return true
}

// runFakeServer reads JSON-RPC messages from stdin and answers them, acting out
// the requested mode. It writes one message per line, which is the stdio
// framing the protocol specifies.
func runFakeServer(mode string) {
	if mode == modeCrash {
		fmt.Fprintln(os.Stderr, "fake server: cannot find module '@example/mcp-server'")
		os.Exit(3)
	}

	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer writer.Flush()

	// The paginating mode hands out its second page only after the first, so
	// the client has to follow the cursor to see every tool.
	page := 0

	for {
		line, err := reader.ReadString('\n')
		if strings.TrimSpace(line) != "" {
			handleLine(writer, strings.TrimSpace(line), mode, &page)
		}
		if err != nil {
			return
		}
	}
}

func handleLine(writer *bufio.Writer, line, mode string, page *int) {
	var message struct {
		ID     *int64          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &message); err != nil {
		return
	}
	if message.ID == nil {
		// A notification, such as notifications/initialized. Nothing to answer.
		return
	}

	switch message.Method {
	case "initialize":
		writeResult(writer, *message.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "fake-server", "version": "9.9"},
		})
	case "tools/list":
		if mode == modeSilent {
			return
		}
		writeTools(writer, *message.ID, mode, *page)
		*page++
	case "tools/call":
		if mode == modeSilent {
			return
		}
		writeCall(writer, *message.ID, mode, message.Params)
	default:
		writeError(writer, *message.ID, -32601, "method not found: "+message.Method)
	}
}

func writeTools(writer *bufio.Writer, id int64, mode string, page int) {
	if mode == modeProse {
		fmt.Fprintln(writer, "starting up, please wait")
		writer.Flush()
		return
	}
	if mode == modeNotification {
		// A server sending a progress notification before its answer is
		// normal, and the client must not mistake it for the response.
		writeRaw(writer, map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{"level": "info", "data": "listing"}})
	}
	switch mode {
	case modeNoTools:
		writeResult(writer, id, map[string]any{"tools": []any{}})
	case modePaginated:
		if page == 0 {
			writeResult(writer, id, map[string]any{
				"tools":      []any{map[string]any{"name": "first", "description": "The first page."}},
				"nextCursor": "page-2",
			})
			return
		}
		writeResult(writer, id, map[string]any{
			"tools": []any{map[string]any{"name": "second", "description": "The second page."}},
		})
	case modeCollision:
		writeResult(writer, id, map[string]any{"tools": []any{
			map[string]any{"name": "scan", "description": "First scan."},
			map[string]any{"name": "scan", "description": "Second scan."},
		}})
	case modeUglyToolName:
		writeResult(writer, id, map[string]any{"tools": []any{
			map[string]any{"name": "read: file/thing", "description": "A name with illegal characters."},
		}})
	default:
		writeResult(writer, id, map[string]any{"tools": []any{
			map[string]any{
				"name":        "echo",
				"description": "Echo the text back.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				},
				"annotations": map[string]any{"readOnlyHint": true},
			},
			map[string]any{
				"name":        "write_it",
				"description": "Change something.",
				"inputSchema": map[string]any{"type": "object"},
			},
		}})
	}
}

func writeCall(writer *bufio.Writer, id int64, mode string, params json.RawMessage) {
	switch mode {
	case modeCallError:
		writeError(writer, id, -32000, "the tool refused to run")
		return
	case modeServerError:
		writeResult(writer, id, map[string]any{
			"isError": true,
			"content": []any{map[string]any{"type": "text", "text": "the target was unreachable"}},
		})
		return
	}

	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	_ = json.Unmarshal(params, &call)
	switch call.Name {
	case "image_only":
		writeResult(writer, id, map[string]any{
			"content": []any{map[string]any{"type": "image", "mimeType": "image/png", "data": "aGk="}},
		})
	case "empty":
		writeResult(writer, id, map[string]any{"content": []any{}})
	default:
		text, _ := call.Arguments["text"].(string)
		writeResult(writer, id, map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "echo: " + text}},
		})
	}
}

func writeResult(writer *bufio.Writer, id int64, result any) {
	writeRaw(writer, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeError(writer *bufio.Writer, id int64, code int, message string) {
	writeRaw(writer, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}

func writeRaw(writer *bufio.Writer, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	writer.Write(encoded)
	writer.WriteByte('\n')
	writer.Flush()
}
