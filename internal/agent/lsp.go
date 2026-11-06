package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// lspSession represents a running language server.
type lspSession struct {
	ID       string
	Name     string
	Language string
	Root     string
	Cmd      *exec.Cmd
	Stdin    ioWriteCloser
	Stdout   ioReadCloser
	Cancel   context.CancelFunc
}

// lspMessage is the base LSP JSON-RPC message.
type lspMessage struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method,omitempty"`
	Params  any    `json:"params,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

var (
	lspSessionsMu sync.Mutex
	lspSessions   = make(map[string]*lspSession)
	lspSeq        int
)

// lspStartTool starts a language server for a workspace.
type lspStartTool struct{}

func (t *lspStartTool) Name() string      { return "lsp_start" }
func (t *lspStartTool) Aliases() []string { return []string{"start_lsp"} }
func (t *lspStartTool) Mutating() bool    { return true }
func (t *lspStartTool) Risk() Risk        { return RiskCommand }
func (t *lspStartTool) Description() string {
	return "Start a language server for a workspace root."
}
func (t *lspStartTool) Schema() map[string]any {
	return object(map[string]any{
		"language": strProp("Language id, e.g. go, python, typescript."),
		"root":     strProp("Workspace root path."),
		"command":  strProp("LSP server command, e.g. gopls, pyright-langserver, typescript-language-server."),
	})
}
func (t *lspStartTool) Label(a map[string]any) string {
	return "Starting LSP " + argString(a, "language")
}
func (t *lspStartTool) DoneLabel(a map[string]any) string {
	return "Started LSP " + argString(a, "language")
}

func (t *lspStartTool) Run(_ context.Context, env *Env, args map[string]any) (Result, error) {
	language := argString(args, "language")
	root := argString(args, "root")
	command := argString(args, "command")
	if language == "" || root == "" || command == "" {
		return Result{Output: "language, root, and command are required", IsError: true}, nil
	}
	if err := checkWorkspacePath(env, root); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, command)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return Result{Output: fmt.Sprintf("stdin pipe: %v", err), IsError: true}, nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return Result{Output: fmt.Sprintf("stdout pipe: %v", err), IsError: true}, nil
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return Result{Output: fmt.Sprintf("start: %v", err), IsError: true}, nil
	}

	lspSeq++
	id := fmt.Sprintf("lsp-%d", lspSeq)
	session := &lspSession{
		ID:       id,
		Name:     language,
		Language: language,
		Root:     root,
		Cmd:      cmd,
		Stdin:    stdin,
		Stdout:   stdout,
		Cancel:   cancel,
	}
	lspSessionsMu.Lock()
	lspSessions[id] = session
	lspSessionsMu.Unlock()

	// Send initialize.
	init := lspMessage{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"processId": os.Getpid(),
			"rootUri":   "file://" + root,
			"capabilities": map[string]any{
				"textDocument": map[string]any{
					"definition": map[string]any{},
					"references": map[string]any{},
					"diagnostic": map[string]any{},
				},
			},
		},
	}
	if err := lspWrite(session, init); err != nil {
		lspStop(session)
		return Result{Output: fmt.Sprintf("initialize write: %v", err), IsError: true}, nil
	}
	// Read initialize response (best effort).
	_, _ = lspRead(session, 3*time.Second)

	return Result{Output: fmt.Sprintf("Started LSP %s for %s (id=%s)", language, root, id)}, nil
}

// lspStopTool stops a language server.
type lspStopTool struct{}

func (t *lspStopTool) Name() string      { return "lsp_stop" }
func (t *lspStopTool) Aliases() []string { return []string{"stop_lsp"} }
func (t *lspStopTool) Mutating() bool    { return true }
func (t *lspStopTool) Risk() Risk        { return RiskCommand }
func (t *lspStopTool) Description() string {
	return "Stop a running language server by id."
}
func (t *lspStopTool) Schema() map[string]any {
	return object(map[string]any{
		"id": strProp("LSP session id from lsp_start."),
	})
}
func (t *lspStopTool) Label(a map[string]any) string {
	return "Stopping LSP " + Shorten(argString(a, "id"), 20)
}
func (t *lspStopTool) DoneLabel(a map[string]any) string {
	return "Stopped LSP " + Shorten(argString(a, "id"), 20)
}

func (t *lspStopTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	id := argString(args, "id")
	if id == "" {
		return Result{Output: "id is required", IsError: true}, nil
	}
	lspSessionsMu.Lock()
	session, ok := lspSessions[id]
	if ok {
		delete(lspSessions, id)
	}
	lspSessionsMu.Unlock()
	if !ok {
		return Result{Output: "LSP session not found", IsError: true}, nil
	}
	lspStop(session)
	return Result{Output: fmt.Sprintf("Stopped LSP %s", id)}, nil
}

// lspDefinitionTool asks for go-to-definition.
type lspDefinitionTool struct{}

func (t *lspDefinitionTool) Name() string      { return "lsp_definition" }
func (t *lspDefinitionTool) Aliases() []string { return []string{"goto_def", "lsp_def"} }
func (t *lspDefinitionTool) Mutating() bool    { return false }
func (t *lspDefinitionTool) Risk() Risk        { return RiskCommand }
func (t *lspDefinitionTool) Description() string {
	return "Find the definition of a symbol at a file location."
}
func (t *lspDefinitionTool) Schema() map[string]any {
	return object(map[string]any{
		"id":   strProp("LSP session id."),
		"path": strProp("File path."),
		"line": intProp("0-based line number."),
		"char": intProp("0-based character offset."),
	})
}
func (t *lspDefinitionTool) Label(a map[string]any) string {
	return "LSP definition " + Shorten(argString(a, "path"), 30)
}
func (t *lspDefinitionTool) DoneLabel(a map[string]any) string {
	return "LSP definition " + Shorten(argString(a, "path"), 30)
}

func (t *lspDefinitionTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	session, err := lspSessionByArg(args)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	msg := lspMessage{
		JSONRPC: "2.0",
		ID:      nextLSPID(),
		Method:  "textDocument/definition",
		Params: map[string]any{
			"textDocument": map[string]any{"uri": "file://" + argString(args, "path")},
			"position": map[string]any{
				"line":      argInt(args, "line", 0, 0, 100000),
				"character": argInt(args, "char", 0, 0, 100000),
			},
		},
	}
	if err := lspWrite(session, msg); err != nil {
		return Result{Output: fmt.Sprintf("write: %v", err), IsError: true}, nil
	}
	var resp lspMessage
	if err := lspReadJSON(session, &resp, 5*time.Second); err != nil {
		return Result{Output: fmt.Sprintf("read: %v", err), IsError: true}, nil
	}
	data, _ := json.MarshalIndent(resp.Result, "", "  ")
	return Result{Output: string(data)}, nil
}

// lspReferencesTool finds references.
type lspReferencesTool struct{}

func (t *lspReferencesTool) Name() string      { return "lsp_references" }
func (t *lspReferencesTool) Aliases() []string { return []string{"find_refs", "lsp_refs"} }
func (t *lspReferencesTool) Mutating() bool    { return false }
func (t *lspReferencesTool) Risk() Risk        { return RiskCommand }
func (t *lspReferencesTool) Description() string {
	return "Find all references to a symbol at a file location."
}
func (t *lspReferencesTool) Schema() map[string]any {
	return object(map[string]any{
		"id":   strProp("LSP session id."),
		"path": strProp("File path."),
		"line": intProp("0-based line number."),
		"char": intProp("0-based character offset."),
	})
}
func (t *lspReferencesTool) Label(a map[string]any) string {
	return "LSP references " + Shorten(argString(a, "path"), 30)
}
func (t *lspReferencesTool) DoneLabel(a map[string]any) string {
	return "LSP references " + Shorten(argString(a, "path"), 30)
}

func (t *lspReferencesTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	session, err := lspSessionByArg(args)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	msg := lspMessage{
		JSONRPC: "2.0",
		ID:      nextLSPID(),
		Method:  "textDocument/references",
		Params: map[string]any{
			"textDocument": map[string]any{"uri": "file://" + argString(args, "path")},
			"position": map[string]any{
				"line":      argInt(args, "line", 0, 0, 100000),
				"character": argInt(args, "char", 0, 0, 100000),
			},
			"context": map[string]any{"includeDeclaration": true},
		},
	}
	if err := lspWrite(session, msg); err != nil {
		return Result{Output: fmt.Sprintf("write: %v", err), IsError: true}, nil
	}
	var resp lspMessage
	if err := lspReadJSON(session, &resp, 5*time.Second); err != nil {
		return Result{Output: fmt.Sprintf("read: %v", err), IsError: true}, nil
	}
	data, _ := json.MarshalIndent(resp.Result, "", "  ")
	return Result{Output: string(data)}, nil
}

// lspDiagnosticsTool reads diagnostics from the session.
type lspDiagnosticsTool struct{}

func (t *lspDiagnosticsTool) Name() string      { return "lsp_diagnostics" }
func (t *lspDiagnosticsTool) Aliases() []string { return []string{"lsp_diag"} }
func (t *lspDiagnosticsTool) Mutating() bool    { return false }
func (t *lspDiagnosticsTool) Risk() Risk        { return RiskCommand }
func (t *lspDiagnosticsTool) Description() string {
	return "Get diagnostics from the active language server."
}
func (t *lspDiagnosticsTool) Schema() map[string]any {
	return object(map[string]any{
		"id":   strProp("LSP session id."),
		"path": strProp("Optional file path to filter diagnostics."),
	})
}
func (t *lspDiagnosticsTool) Label(a map[string]any) string {
	return "LSP diagnostics"
}
func (t *lspDiagnosticsTool) DoneLabel(a map[string]any) string {
	return "LSP diagnostics"
}

func (t *lspDiagnosticsTool) Run(_ context.Context, _ *Env, args map[string]any) (Result, error) {
	session, err := lspSessionByArg(args)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	// Request diagnostics for a specific file or the whole root.
	uri := "file://" + argString(args, "path")
	if uri == "file://" {
		uri = "file://" + session.Root + "/"
	}
	msg := lspMessage{
		JSONRPC: "2.0",
		ID:      nextLSPID(),
		Method:  "textDocument/diagnostic",
		Params: map[string]any{
			"textDocument": map[string]any{"uri": uri},
		},
	}
	if err := lspWrite(session, msg); err != nil {
		return Result{Output: fmt.Sprintf("write: %v", err), IsError: true}, nil
	}
	var resp lspMessage
	if err := lspReadJSON(session, &resp, 5*time.Second); err != nil {
		return Result{Output: fmt.Sprintf("read: %v", err), IsError: true}, nil
	}
	data, _ := json.MarshalIndent(resp.Result, "", "  ")
	return Result{Output: string(data)}, nil
}

// lspSessionByArg resolves a session id from args or returns the only session.
func lspSessionByArg(args map[string]any) (*lspSession, error) {
	id := argString(args, "id")
	lspSessionsMu.Lock()
	defer lspSessionsMu.Unlock()
	if id != "" {
		if s, ok := lspSessions[id]; ok {
			return s, nil
		}
		return nil, fmt.Errorf("LSP session %q not found", id)
	}
	if len(lspSessions) == 0 {
		return nil, fmt.Errorf("no active LSP sessions")
	}
	if len(lspSessions) == 1 {
		for _, s := range lspSessions {
			return s, nil
		}
	}
	return nil, fmt.Errorf("multiple LSP sessions active; specify id")
}

var lspCounter int

func nextLSPID() int {
	lspCounter++
	return lspCounter
}

func lspStop(session *lspSession) {
	if session == nil {
		return
	}
	session.Cancel()
	_ = session.Cmd.Process.Kill()
	_ = session.Stdin.Close()
	_ = session.Stdout.Close()
}

// ioWriteCloser abstracts stdin for testability.
type ioWriteCloser interface {
	Write([]byte) (int, error)
	Close() error
}

// ioReadCloser abstracts stdout for testability.
type ioReadCloser interface {
	Read([]byte) (int, error)
	Close() error
}

func lspWrite(session *lspSession, msg lspMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	_, err = session.Stdin.Write([]byte(header))
	if err != nil {
		return err
	}
	_, err = session.Stdin.Write(data)
	return err
}

func lspRead(session *lspSession, timeout time.Duration) ([]byte, error) {
	buf := make([]byte, 4096)
	// Timeout is best-effort; the underlying pipe may not support SetReadDeadline.
	n, err := session.Stdout.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func lspReadJSON(session *lspSession, target any, timeout time.Duration) error {
	// Read header first (best-effort).
	buf := make([]byte, 4096)
	n, err := session.Stdout.Read(buf)
	if err != nil {
		return err
	}
	// Look for JSON body after headers.
	body := buf[n:]
	if idx := strings.Index(string(body), "\r\n\r\n"); idx >= 0 {
		body = body[idx+4:]
	}
	if idx := strings.Index(string(body), "\n\n"); idx >= 0 {
		body = body[idx+2:]
	}
	return json.Unmarshal(body, target)
}
