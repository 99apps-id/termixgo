// Package lsp is a small Language Server Protocol client. It starts a language
// server over stdio, keeps the files the agent asked about open on it, and
// exposes the requests a coding agent needs: definition, references, hover,
// symbols, diagnostics and rename.
//
// Only what those requests need is implemented. Server-to-client requests are
// answered with empty results, because a server that waits on an unanswered
// workspace/configuration request never replies to anything else.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/proc"
)

// maxMessageBytes bounds one message from the server. A references answer on a
// large repository is a few megabytes; anything bigger is a broken stream.
const maxMessageBytes = 64 << 20

// Client is one running language server.
type Client struct {
	name string
	root string

	cmd   *exec.Cmd
	stdin io.WriteCloser

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan response
	closed  bool
	done    chan struct{}
	readErr error

	// opened maps a document URI to the version and text last sent, so a file
	// changed on disk is resent instead of answered from stale text.
	opened map[string]openDoc
	// diags holds the newest diagnostics per URI and a counter that moves on
	// every publish, which is how a caller waits for a fresh report.
	diags     map[string][]Diagnostic
	diagSeq   map[string]int
	diagWaker chan struct{}

	stderr *tailBuffer
}

type openDoc struct {
	version int
	text    string
}

type response struct {
	Result json.RawMessage
	Error  *rpcError
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// Start launches a server and completes the initialize handshake.
func Start(ctx context.Context, name string, argv []string, root string) (*Client, error) {
	if len(argv) == 0 {
		return nil, errors.New("no language server command")
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, fmt.Errorf("%s is not installed (%s not found on PATH)", name, argv[0])
	}
	cmd := exec.Command(path, argv[1:]...)
	proc.Detach(cmd)
	cmd.Dir = root
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tail := &tailBuffer{limit: 4096}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	client := &Client{
		name:      name,
		root:      root,
		cmd:       cmd,
		stdin:     stdin,
		pending:   map[int64]chan response{},
		done:      make(chan struct{}),
		opened:    map[string]openDoc{},
		diags:     map[string][]Diagnostic{},
		diagSeq:   map[string]int{},
		diagWaker: make(chan struct{}),
		stderr:    tail,
	}
	go client.read(stdout)
	go func() { _ = cmd.Wait() }()

	if err := client.initialize(ctx); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

func (c *Client) initialize(ctx context.Context) error {
	rootURI := PathToURI(c.root)
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   rootURI,
		"rootPath":  c.root,
		"workspaceFolders": []map[string]any{
			{"uri": rootURI, "name": "workspace"},
		},
		"capabilities": map[string]any{
			"general": map[string]any{"positionEncodings": []string{"utf-16"}},
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"didSave": false},
				"definition":         map[string]any{"linkSupport": false},
				"references":         map[string]any{},
				"hover":              map[string]any{"contentFormat": []string{"plaintext", "markdown"}},
				"documentSymbol":     map[string]any{"hierarchicalDocumentSymbolSupport": true},
				"rename":             map[string]any{"prepareSupport": false},
				"publishDiagnostics": map[string]any{"relatedInformation": false},
			},
			"workspace": map[string]any{
				"symbol":           map[string]any{},
				"workspaceEdit":    map[string]any{"documentChanges": true},
				"configuration":    true,
				"workspaceFolders": true,
			},
			"window": map[string]any{"workDoneProgress": false},
		},
	}
	if err := c.Request(ctx, "initialize", params, nil); err != nil {
		return fmt.Errorf("%s initialize: %w", c.name, err)
	}
	return c.Notify("initialized", map[string]any{})
}

// Dead reports whether the server stopped answering for good.
func (c *Client) Dead() bool {
	select {
	case <-c.done:
		return true
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Request sends one request and decodes its result into out (which may be nil).
func (c *Client) Request(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return fmt.Errorf("%s is closed", c.name)
	}
	return c.call(ctx, method, params, out)
}

// call is Request without the closed check, so Close can still say shutdown.
func (c *Client) call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	waiter := make(chan response, 1)
	c.pending[id] = waiter
	c.mu.Unlock()

	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		c.forget(id)
		return err
	}
	select {
	case <-ctx.Done():
		c.forget(id)
		_ = c.Notify("$/cancelRequest", map[string]any{"id": id})
		return ctx.Err()
	case <-c.done:
		return c.deadError()
	case reply := <-waiter:
		if reply.Error != nil {
			return reply.Error
		}
		if out != nil && len(reply.Result) > 0 && string(reply.Result) != "null" {
			if err := json.Unmarshal(reply.Result, out); err != nil {
				return fmt.Errorf("%s %s: unreadable result: %w", c.name, method, err)
			}
		}
		return nil
	}
}

// Notify sends a notification, which has no answer.
func (c *Client) Notify(method string, params any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) deadError() error {
	c.mu.Lock()
	reason := c.readErr
	c.mu.Unlock()
	detail := strings.TrimSpace(c.stderr.String())
	if detail != "" {
		detail = ": " + lastLine(detail)
	}
	if reason != nil && !errors.Is(reason, io.EOF) {
		return fmt.Errorf("%s stopped (%v)%s", c.name, reason, detail)
	}
	return fmt.Errorf("%s stopped%s", c.name, detail)
}

func (c *Client) send(payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return fmt.Errorf("%s closed its input: %w", c.name, err)
	}
	if _, err := c.stdin.Write(body); err != nil {
		return fmt.Errorf("%s closed its input: %w", c.name, err)
	}
	return nil
}

func (c *Client) read(stdout io.Reader) {
	reader := bufio.NewReaderSize(stdout, 64*1024)
	for {
		body, err := readMessage(reader)
		if err != nil {
			c.mu.Lock()
			c.readErr = err
			for id, waiter := range c.pending {
				close(waiter)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			close(c.done)
			return
		}
		c.dispatch(body)
	}
}

// readMessage reads one Content-Length framed message.
func readMessage(reader *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if length < 0 {
				// A blank line before any header is noise some servers print.
				continue
			}
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed < 0 {
				return nil, fmt.Errorf("bad Content-Length %q", value)
			}
			length = parsed
		}
	}
	if length > maxMessageBytes {
		return nil, fmt.Errorf("message of %d bytes is over the limit", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	return body, nil
}

func (c *Client) dispatch(body []byte) {
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if json.Unmarshal(body, &message) != nil {
		return
	}
	hasID := len(message.ID) > 0 && string(message.ID) != "null"
	switch {
	case message.Method != "" && hasID:
		c.answerServerRequest(message.ID, message.Method, message.Params)
	case message.Method != "":
		if message.Method == "textDocument/publishDiagnostics" {
			c.storeDiagnostics(message.Params)
		}
	case hasID:
		id, err := strconv.ParseInt(strings.Trim(string(message.ID), `"`), 10, 64)
		if err != nil {
			return
		}
		c.mu.Lock()
		waiter := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if waiter != nil {
			waiter <- response{Result: message.Result, Error: message.Error}
		}
	}
}

// answerServerRequest replies to a request the server sent. Configuration
// gets one null per item, everything else an empty success.
func (c *Client) answerServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	var result any
	if method == "workspace/configuration" {
		var request struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(params, &request)
		result = make([]any, len(request.Items))
	}
	_ = c.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (c *Client) storeDiagnostics(params json.RawMessage) {
	var report struct {
		URI         string       `json:"uri"`
		Diagnostics []Diagnostic `json:"diagnostics"`
	}
	if json.Unmarshal(params, &report) != nil {
		return
	}
	key := normalizeURI(report.URI)
	c.mu.Lock()
	c.diags[key] = report.Diagnostics
	c.diagSeq[key]++
	close(c.diagWaker)
	c.diagWaker = make(chan struct{})
	c.mu.Unlock()
}

// Sync opens a file on the server, or resends it when the text on disk
// changed since the last send. It returns the file's text and whether
// anything was sent.
func (c *Client) Sync(path, languageID string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	text := string(data)
	uri := PathToURI(path)
	key := normalizeURI(uri)
	c.mu.Lock()
	doc, open := c.opened[key]
	c.mu.Unlock()
	switch {
	case !open:
		err = c.Notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{"uri": uri, "languageId": languageID, "version": 1, "text": text},
		})
		doc = openDoc{version: 1, text: text}
	case doc.text != text:
		doc.version++
		doc.text = text
		err = c.Notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": doc.version},
			"contentChanges": []map[string]any{{"text": text}},
		})
	default:
		return text, false, nil
	}
	if err != nil {
		return "", false, err
	}
	c.mu.Lock()
	c.opened[key] = doc
	c.mu.Unlock()
	return text, true, nil
}

// DiagnosticsSeq reports how many diagnostic reports a file has received.
func (c *Client) DiagnosticsSeq(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.diagSeq[normalizeURI(PathToURI(path))]
}

// WaitDiagnostics returns a file's diagnostics once a report newer than seq
// arrives, or whatever is known when the wait ends. The bool reports whether a
// fresh report arrived.
func (c *Client) WaitDiagnostics(ctx context.Context, path string, seq int, wait time.Duration) ([]Diagnostic, bool) {
	key := normalizeURI(PathToURI(path))
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		current := c.diagSeq[key]
		diags := append([]Diagnostic(nil), c.diags[key]...)
		waker := c.diagWaker
		c.mu.Unlock()
		if current > seq {
			return diags, true
		}
		select {
		case <-waker:
		case <-deadline.C:
			return diags, false
		case <-ctx.Done():
			return diags, false
		case <-c.done:
			return diags, false
		}
	}
}

// Close shuts the server down politely, then kills it.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	gone := false
	select {
	case <-c.done:
		gone = true
	default:
	}
	if !gone {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.call(ctx, "shutdown", nil, nil)
		cancel()
		_ = c.Notify("exit", nil)
	}
	_ = c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(time.Second):
	}
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

// tailBuffer keeps the last bytes a server wrote to stderr.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data = append(t.data, p...)
	if len(t.data) > t.limit {
		t.data = t.data[len(t.data)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.data)
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
