package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// defaultTimeout bounds a single request. A tool call runs its own deadline
// through the caller's context, so this is the handshake and listing bound.
const defaultTimeout = 30 * time.Second

// stderrBufferBytes is how much of the server's stderr is kept for diagnosis.
// A server that fails at startup explains itself there, and the last few
// kilobytes are what names the missing package or the bad argument.
const stderrBufferBytes = 8192

// maxMessageBytes bounds one JSON-RPC line on the server's stdout. A protocol
// violation or a server dumping binary to stdout would otherwise grow without
// limit and exhaust memory.
const maxMessageBytes = 4 << 20

// Options describe how to start one MCP server.
type Options struct {
	// Name is the label the operator uses and the prefix of every tool the
	// server contributes.
	Name string
	// Command is the executable to run.
	Command string
	Args    []string
	// Env adds to the inherited environment rather than replacing it: an MCP
	// server needs PATH to find its own interpreter.
	Env map[string]string
	// Dir is the working directory. Empty means the session workspace.
	Dir string
}

// Client is one running MCP server, seen as a JSON-RPC peer on its stdio.
type Client struct {
	name    string
	command string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *ringBuffer

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan response
	done    chan struct{}
	readErr error
	closed  bool

	version    string
	serverName string

	// tools caches what tools/list returned, so a reload does not have to ask
	// again for a listing that cannot change while the process lives.
	tools []Tool
}

// Start launches the server and completes the handshake.
func Start(ctx context.Context, options Options) (*Client, error) {
	if strings.TrimSpace(options.Command) == "" {
		return nil, errors.New("an MCP server needs a command to run; set command in the MCP config")
	}
	command := exec.Command(options.Command, options.Args...)
	command.Dir = options.Dir
	command.Env = mergeEnv(os.Environ(), options.Env)
	// The server speaks a protocol on stdout, so anything it prints there for
	// people would corrupt the stream. stderr is where its diagnostics belong,
	// and that is what gets kept for a failure message.
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdin for %s: %w", options.Name, err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdout for %s: %w", options.Name, err)
	}
	stderr := newRingBuffer(stderrBufferBytes)
	command.Stderr = stderr

	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", options.Command, err)
	}

	client := &Client{
		name:    options.Name,
		command: options.Command,
		cmd:     command,
		stdin:   stdin,
		stderr:  stderr,
		pending: map[int64]chan response{},
		done:    make(chan struct{}),
	}
	go client.read(stdout)

	if err := client.initialize(ctx); err != nil {
		// Close reaps the process, which also drains the goroutine copying its
		// stderr. That copy is asynchronous, so on a crash during the handshake
		// the error can be built before the server's explanation reaches the
		// buffer. Rebuild it now that the buffer is complete, so the operator
		// sees the missing module instead of a bare closed pipe.
		client.Close()
		if detail := strings.TrimSpace(client.Stderr()); detail != "" {
			short := shortLine(detail)
			if !strings.Contains(err.Error(), short) {
				return nil, fmt.Errorf("%w: %s", err, short)
			}
		}
		return nil, err
	}
	return client, nil
}

// Name is the operator-facing label of this server.
func (c *Client) Name() string { return c.name }

// ServerName is the name the server calls itself, which can differ from the
// configured label and is worth showing when it does.
func (c *Client) ServerName() string { return c.serverName }

// ProtocolVersion is the revision the server agreed to use.
func (c *Client) ProtocolVersion() string { return c.version }

// Stderr returns what the server has written to stderr, for diagnosis.
func (c *Client) Stderr() string { return c.stderr.String() }

// readLineWithLimit reads one JSON-RPC frame from stdout, capping the line
// length so a misbehaving server cannot allocate unbounded memory.
//
// A frame is not bounded by the reader's buffer: a server's tools/list or a
// large tool result is one line far bigger than the 4 KB bufio default.
// ReadSlice stops at the buffer edge and reports ErrBufferFull, so the
// fragments are collected until the newline arrives rather than treated as an
// overlong message. Only a frame past the hard cap is refused.
func (c *Client) readLineWithLimit(reader *bufio.Reader) ([]byte, error) {
	var frame []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(frame)+len(fragment) > maxMessageBytes {
			return nil, fmt.Errorf("%s produced a message over %d bytes", c.name, maxMessageBytes)
		}
		frame = append(frame, fragment...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return frame, err
		}
		return frame, nil
	}
}

// read decodes one JSON-RPC message per line until the stream ends.
//
// A message without an id is a notification, and one carrying a method is a
// request from the server. Neither has an answer the agent asked for, so both
// are dropped rather than treated as a protocol failure: refusing to tolerate
// them would break every server that sends progress notifications.
func (c *Client) read(stdout io.ReadCloser) {
	defer close(c.done)
	reader := bufio.NewReader(stdout)
	for {
		line, err := c.readLineWithLimit(reader)
		if len(line) > 0 {
			c.deliver(line)
		}
		if err != nil {
			var reason error
			if !errors.Is(err, io.EOF) {
				reason = err
			}
			c.abandon(reason)
			return
		}
	}
}

// abandon fails every waiting request. A stream that ended or was poisoned can
// never answer again, so waiting for each request's own deadline would just
// make the operator sit through a timeout for a failure already known.
func (c *Client) abandon(reason error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if reason != nil && c.readErr == nil {
		c.readErr = reason
	}
	for id, waiter := range c.pending {
		close(waiter)
		delete(c.pending, id)
	}
}

// deliver routes one message to whoever is waiting for its id.
func (c *Client) deliver(line []byte) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return
	}
	var message response
	if err := json.Unmarshal([]byte(trimmed), &message); err != nil {
		// A line that is not JSON is the server writing prose to stdout. It
		// means the stream is poisoned: the next message cannot be trusted to
		// be the start of a frame, so every request fails now.
		c.abandon(fmt.Errorf("%s wrote a non-JSON line to stdout: %s", c.name, shortLine(trimmed)))
		return
	}
	if message.ID == nil {
		return
	}
	c.mu.Lock()
	waiter, ok := c.pending[*message.ID]
	if ok {
		delete(c.pending, *message.ID)
	}
	c.mu.Unlock()
	if ok {
		waiter <- message
	}
}

// exchange sends one request and waits for its answer.
func (c *Client) exchange(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("%s is closed", c.name)
	}
	c.nextID++
	id := c.nextID
	waiter := make(chan response, 1)
	c.pending[id] = waiter
	c.mu.Unlock()

	payload, err := json.Marshal(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		c.forget(id)
		return nil, fmt.Errorf("encode %s: %w", method, err)
	}
	if err := c.write(payload); err != nil {
		c.forget(id)
		return nil, err
	}

	select {
	case message, ok := <-waiter:
		if !ok {
			// The reader closed the channel, which means the process died.
			return nil, c.closedError(method)
		}
		if message.Error != nil {
			return nil, fmt.Errorf("%s %s: %w", c.name, method, message.Error)
		}
		if len(message.Result) == 0 {
			return json.RawMessage("{}"), nil
		}
		return message.Result, nil
	case <-ctx.Done():
		c.forget(id)
		return nil, fmt.Errorf("%s %s: %w", c.name, method, ctx.Err())
	case <-c.done:
		c.forget(id)
		return nil, c.closedError(method)
	}
}

// notify sends a message that expects no answer.
func (c *Client) notify(method string, params any) error {
	payload, err := json.Marshal(request{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("encode %s: %w", method, err)
	}
	return c.write(payload)
}

// write sends one newline-delimited message. The protocol frames messages by
// line, so a payload has to be free of raw newlines, which json.Marshal
// guarantees.
func (c *Client) write(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("%s closed its input: %w", c.name, err)
	}
	return nil
}

// forget drops a pending request, which is what a timeout has to do or the map
// would grow for the life of the session.
func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// closedError explains why a call could not finish, using the server's own
// stderr when there is any. Without that the operator sees "broken pipe" and
// has to guess which package is missing.
func (c *Client) closedError(method string) error {
	c.mu.Lock()
	readErr := c.readErr
	c.mu.Unlock()
	detail := ""
	if text := strings.TrimSpace(c.Stderr()); text != "" {
		detail = ": " + shortLine(text)
	}
	switch {
	case readErr != nil && !errors.Is(readErr, io.EOF):
		return fmt.Errorf("%s %s failed%s (%v)", c.name, method, detail, readErr)
	case readErr != nil:
		return fmt.Errorf("%s stopped before answering %s%s", c.name, method, detail)
	default:
		return fmt.Errorf("%s is closed%s", c.name, detail)
	}
}

// initialize performs the handshake: negotiate a revision and tell the server
// the client is ready.
func (c *Client) initialize(ctx context.Context) error {
	callCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
	raw, err := c.exchange(callCtx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": ClientName, "version": ClientVersion},
	})
	if err != nil {
		return err
	}
	var handshake InitializeResult
	if err := json.Unmarshal(raw, &handshake); err != nil {
		return fmt.Errorf("%s sent an unreadable initialize result: %w", c.name, err)
	}
	c.version = handshake.ProtocolVersion
	c.serverName = handshake.ServerInfo.Name
	// The notification is not optional: a server that never receives it is
	// entitled to keep waiting before it answers anything else.
	return c.notify("notifications/initialized", map[string]any{})
}

// Tools lists the server's tools, following pagination to the end.
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	if len(c.tools) > 0 {
		return c.tools, nil
	}
	var all []Tool
	cursor := ""
	for {
		callCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.exchange(callCtx, "tools/list", params)
		cancel()
		if err != nil {
			return nil, err
		}
		var listing ListToolsResult
		if err := json.Unmarshal(raw, &listing); err != nil {
			return nil, fmt.Errorf("%s sent an unreadable tools/list result: %w", c.name, err)
		}
		all = append(all, listing.Tools...)
		if listing.NextCursor == "" {
			break
		}
		cursor = listing.NextCursor
	}
	c.tools = all
	return all, nil
}

// Call runs one tool. The caller's context is the deadline, so a long scan is
// bounded by the tool timeout the rest of the agent already applies.
func (c *Client) Call(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.exchange(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return CallResult{}, err
	}
	var result CallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return CallResult{}, fmt.Errorf("%s sent an unreadable tools/call result: %w", c.name, err)
	}
	return result, nil
}

// Close stops the server.
//
// Closing stdin first gives the server the chance to exit on its own, which is
// how a well-behaved one flushes its state. The process is then killed because
// a server that ignores a closed stdin would outlive the session and hold its
// port or its lock. Wait then reaps it: without the call the process stays
// unreaped for the life of the program, which leaks a handle and a pid on every
// reload. Note that this kills the process, not a tree: a server started through
// a launcher that spawns a child of its own can leave that child behind.
// Closing stdin is what usually prevents it.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	_ = c.stdin.Close()
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		// Wait reaps the process. It also closes the stdout pipe, which is what
		// unblocks the reader goroutine if it is still waiting on it.
		_ = c.cmd.Wait()
	}
}

// shortLine collapses a multi-line message into one line and clips it, so a
// stderr dump or a stray stdout line fits in an error the UI can render.
//
// The clip moves back to a rune boundary: a server's stderr is free text and may
// be in any language, and a byte slice at a fixed offset can land inside a
// multi-byte character and hand the terminal invalid UTF-8.
func shortLine(text string) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if len(collapsed) <= 400 {
		return collapsed
	}
	cut := 400
	for cut > 0 && !utf8.RuneStart(collapsed[cut]) {
		cut--
	}
	return collapsed[:cut] + "..."
}

// mergeEnv layers extra variables over an inherited environment. A key that
// appears twice is undefined behaviour in the child, so an override replaces
// the inherited entry instead of being appended after it.
func mergeEnv(inherited []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return inherited
	}
	merged := make([]string, 0, len(inherited)+len(extra))
	for _, entry := range inherited {
		key, _, found := strings.Cut(entry, "=")
		if found {
			if _, replaced := extra[key]; replaced {
				continue
			}
		}
		merged = append(merged, entry)
	}
	for key, value := range extra {
		merged = append(merged, key+"="+value)
	}
	return merged
}

// ringBuffer keeps the last n bytes written to it. A server's stderr is
// unbounded and only its tail matters, so keeping the head would be useless.
type ringBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func newRingBuffer(limit int) *ringBuffer { return &ringBuffer{limit: limit} }

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.limit {
		r.buf = r.buf[len(r.buf)-r.limit:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
