package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// writeTimeout bounds one frame write. Chrome reads its socket promptly; a
// write that blocks means the process is gone or wedged.
const writeTimeout = 10 * time.Second

// cdpConn is one DevTools WebSocket. Responses are matched to their request by
// id; events are read and dropped, because every action polls the page state it
// needs rather than waiting on an event stream that a navigation can interrupt.
type cdpConn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex

	mu       sync.Mutex
	nextID   int
	pending  map[int]chan cdpMessage
	closed   bool
	closeErr error
	done     chan struct{}

	targetID string
}

type cdpMessage struct {
	ID     int             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *cdpError       `json:"error,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

// dialCDP connects to one page target.
func dialCDP(ctx context.Context, rawURL string) (*cdpConn, error) {
	if rawURL == "" {
		return nil, errors.New("the page target published no WebSocket URL")
	}
	ws, _, err := websocket.Dial(ctx, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to the browser's debugger: %v", err)
	}
	conn := &cdpConn{
		ws:      ws,
		pending: map[int]chan cdpMessage{},
		done:    make(chan struct{}),
	}
	if id := targetIDFromURL(rawURL); id != "" {
		conn.targetID = id
	}
	go conn.readLoop()
	return conn, nil
}

// targetIDFromURL reads the target id out of a debugger URL, which is useful
// for naming the tab without a second request.
func targetIDFromURL(raw string) string {
	index := strings.Index(raw, "://")
	if index < 0 {
		return ""
	}
	rest := raw[index+3:]
	if query := strings.IndexByte(rest, '?'); query >= 0 {
		rest = rest[:query]
	}
	slash := strings.LastIndexByte(rest, '/')
	if slash < 0 || slash+1 >= len(rest) {
		return ""
	}
	return rest[slash+1:]
}

func (c *cdpConn) readLoop() {
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			c.shutdown(err)
			return
		}
		var message cdpMessage
		if json.Unmarshal(data, &message) != nil {
			continue
		}
		if message.ID == 0 {
			// An event. Nothing here depends on one, so it is dropped after
			// the read, which is what keeps the socket draining.
			continue
		}
		c.mu.Lock()
		channel := c.pending[message.ID]
		delete(c.pending, message.ID)
		c.mu.Unlock()
		if channel != nil {
			channel <- message
		}
	}
}

func (c *cdpConn) shutdown(reason error) {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.closeErr = reason
		close(c.done)
	}
	c.mu.Unlock()
}

// Close shuts the socket down.
func (c *cdpConn) Close() {
	_ = c.ws.Close(websocket.StatusNormalClosure, "")
	c.shutdown(errors.New("the browser connection was closed"))
}

// call sends one command and waits for its response.
func (c *cdpConn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		if err == nil {
			err = errors.New("the browser connection is closed")
		}
		return nil, err
	}
	c.nextID++
	id := c.nextID
	channel := make(chan cdpMessage, 1)
	c.pending[id] = channel
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	c.writeMu.Lock()
	err = c.ws.Write(writeCtx, websocket.MessageText, data)
	c.writeMu.Unlock()
	cancel()
	if err != nil {
		return nil, fmt.Errorf("cannot send %s to the browser: %v", method, err)
	}

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%s did not answer: %v", method, ctx.Err())
	case <-c.done:
		c.mu.Lock()
		reason := c.closeErr
		c.mu.Unlock()
		if reason == nil {
			reason = errors.New("the browser connection closed")
		}
		return nil, fmt.Errorf("%s was interrupted: %v", method, reason)
	case message := <-channel:
		if message.Error != nil {
			detail := message.Error.Message
			if message.Error.Data != "" {
				detail += ": " + message.Error.Data
			}
			return nil, fmt.Errorf("the browser refused %s: %s", method, detail)
		}
		return message.Result, nil
	}
}
