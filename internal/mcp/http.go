package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// httpCloseTimeout bounds the best-effort session termination. The operator is
// already stopping the session; a slow remote server must not hold that up.
const httpCloseTimeout = 5 * time.Second

// isHTTP reports whether this server is reached over the network. An explicit
// transport wins; otherwise a URL with no command means HTTP.
func isHTTP(options Options) bool {
	switch strings.ToLower(strings.TrimSpace(options.Transport)) {
	case "http", "streamable-http", "sse":
		return true
	case "stdio", "command":
		return false
	}
	return strings.TrimSpace(options.URL) != ""
}

// sessionExpiredError marks a 404, which the streamable HTTP transport uses to
// say the session id is gone. The caller re-initializes and retries once.
type sessionExpiredError struct{ detail string }

func (e *sessionExpiredError) Error() string { return e.detail }

// startHTTP validates the endpoint and completes the handshake. No process is
// started: the server runs wherever the operator points the URL.
func startHTTP(ctx context.Context, options Options) (*Client, error) {
	endpoint := strings.TrimSpace(options.URL)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("%s needs an http or https URL, got %q", options.Name, endpoint)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("%s needs an http or https URL, not %q", options.Name, parsed.Scheme)
	}
	client := &Client{
		name:       options.Name,
		command:    endpoint,
		url:        endpoint,
		httpClient: &http.Client{},
		headers:    options.Headers,
		pending:    map[int64]chan response{},
		done:       make(chan struct{}),
	}
	if err := client.initialize(ctx); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// httpExchange sends one request over the streamable HTTP transport.
func (c *Client) httpExchange(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("%s is closed", c.name)
	}
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	payload, err := json.Marshal(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", method, err)
	}
	result, err := c.httpRoundTrip(ctx, payload, id)
	var expired *sessionExpiredError
	if err != nil && errors.As(err, &expired) && method != "initialize" {
		// The server forgot the session (a restart, a load balancer, an idle
		// eviction). One handshake and one retry is the whole recovery: a
		// second 404 is a real failure and is reported as one.
		c.forgetSession()
		if initErr := c.initialize(ctx); initErr != nil {
			return nil, fmt.Errorf("%s lost the session and could not start a new one: %w", c.name, initErr)
		}
		result, err = c.httpRoundTrip(ctx, payload, id)
	}
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return json.RawMessage("{}"), nil
	}
	return result, nil
}

// httpRoundTrip posts one JSON-RPC message and decodes its answer. id 0 means a
// notification: the server acknowledges with 202 and sends no result.
func (c *Client) httpRoundTrip(ctx context.Context, payload []byte, id int64) (json.RawMessage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	for key, value := range c.headers {
		request.Header.Set(key, value)
	}
	c.mu.Lock()
	session := c.sessionID
	c.mu.Unlock()
	if session != "" {
		request.Header.Set("Mcp-Session-Id", session)
	}

	httpResponse, err := c.httpClient.Do(request)
	if err != nil {
		c.setHTTPError(err)
		return nil, fmt.Errorf("%s did not answer: %w", c.name, err)
	}
	defer httpResponse.Body.Close()
	if fresh := strings.TrimSpace(httpResponse.Header.Get("Mcp-Session-Id")); fresh != "" {
		c.mu.Lock()
		c.sessionID = fresh
		c.mu.Unlock()
	}

	switch {
	case httpResponse.StatusCode == http.StatusAccepted || httpResponse.StatusCode == http.StatusNoContent:
		return nil, nil
	case httpResponse.StatusCode == http.StatusOK:
	case httpResponse.StatusCode == http.StatusNotFound:
		body, _ := io.ReadAll(io.LimitReader(httpResponse.Body, 2048))
		detail := fmt.Sprintf("%s returned HTTP 404: %s", c.name, shortLine(string(body)))
		c.setHTTPError(errors.New(detail))
		return nil, &sessionExpiredError{detail: detail}
	default:
		body, _ := io.ReadAll(io.LimitReader(httpResponse.Body, 2048))
		detail := fmt.Sprintf("%s returned HTTP %d: %s", c.name, httpResponse.StatusCode, shortLine(string(body)))
		c.setHTTPError(errors.New(detail))
		return nil, errors.New(detail)
	}

	if strings.Contains(strings.ToLower(httpResponse.Header.Get("Content-Type")), "text/event-stream") {
		return c.readSSE(httpResponse.Body, id)
	}
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxMessageBytes))
	if err != nil {
		return nil, fmt.Errorf("%s sent an unreadable body: %w", c.name, err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, nil
	}
	var message response
	if err := json.Unmarshal(body, &message); err != nil {
		return nil, fmt.Errorf("%s sent a body that is not JSON-RPC: %w", c.name, err)
	}
	if message.Error != nil {
		return nil, fmt.Errorf("%s: %w", c.name, message.Error)
	}
	return message.Result, nil
}

// readSSE reads an event stream until the response with the matching id
// arrives. Progress notifications and events for other ids are skipped: the
// transport lets a server interleave them, and treating one as the answer would
// hand the model the wrong object.
func (c *Client) readSSE(reader io.Reader, id int64) (json.RawMessage, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxMessageBytes)
	var data strings.Builder
	sawResponse := false
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			if strings.HasPrefix(line, "data:") {
				fragment := strings.TrimPrefix(line, "data:")
				fragment = strings.TrimPrefix(fragment, " ")
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(fragment)
			}
			continue
		}
		if raw, matched, ok := matchSSEEvent(data.String(), id); matched {
			if ok {
				return raw, nil
			}
			sawResponse = true
		}
		data.Reset()
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s event stream failed: %w", c.name, err)
	}
	// The last event may arrive without the blank line that ends a block.
	if raw, matched, ok := matchSSEEvent(data.String(), id); matched {
		if ok {
			return raw, nil
		}
		sawResponse = true
	}
	if sawResponse {
		return nil, fmt.Errorf("%s answered with a different request id", c.name)
	}
	return nil, fmt.Errorf("%s closed the event stream without answering", c.name)
}

// matchSSEEvent reports whether one event is a JSON-RPC response, whether it
// belongs to this request, and the result when it does.
func matchSSEEvent(data string, id int64) (json.RawMessage, bool, bool) {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return nil, false, false
	}
	var message response
	if json.Unmarshal([]byte(trimmed), &message) != nil || message.ID == nil {
		return nil, false, false
	}
	if *message.ID != id {
		return nil, true, false
	}
	if message.Error != nil {
		return nil, true, false
	}
	return message.Result, true, true
}

// forgetSession drops the id so the next request starts a new handshake.
func (c *Client) forgetSession() {
	c.mu.Lock()
	c.sessionID = ""
	c.mu.Unlock()
}

// setHTTPError keeps the last transport failure for the server status view.
func (c *Client) setHTTPError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.httpErr = err.Error()
	c.mu.Unlock()
}

// closeHTTP terminates the remote session. DELETE is the transport's explicit
// shutdown; a server that does not answer is simply left to expire.
func (c *Client) closeHTTP() {
	c.mu.Lock()
	session := c.sessionID
	c.mu.Unlock()
	if session == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), httpCloseTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url, nil)
	if err != nil {
		return
	}
	request.Header.Set("Mcp-Session-Id", session)
	for key, value := range c.headers {
		request.Header.Set(key, value)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	response.Body.Close()
}
