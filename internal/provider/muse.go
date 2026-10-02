package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
)

// museUserAgent is the identity Meta's Muse backend expects. It gates access,
// so the official CLI's build string is reused rather than the Termixgo agent.
const museUserAgent = "muse-build/1.3.0 (interactive; macos-aarch64; build ac7280f2aca67769d1455a8847bb502b617d50f6)"

// museClient speaks the Responses API served at api.meta.ai for a Muse login.
// It is not chat-completions: the request carries input items and the stream is
// a typed event feed, so it shares the Codex encoder and decoder. Only the
// headers differ, plus the absence of the responses-lite rewrite.
type museClient struct {
	*httpClient

	mu        sync.Mutex
	sessionID string
}

func (c *museClient) session() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessionID == "" {
		c.sessionID = c.SessionID()
	}
	return c.sessionID
}

func (c *museClient) resetSession() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = deriveSessionID(c.apiKey)
	return c.sessionID
}

func (c *museClient) Stream(ctx context.Context, req ChatRequest, emit func(StreamEvent) error) error {
	payload := c.payload(req)

	key := c.currentKey()
	response, err := c.send(ctx, key, payload)
	if err == nil {
		defer response.Body.Close()
		return decodeResponsesStream(c.info.Label, response.Body, emit)
	}

	// Token expired or session invalidated on Meta's backend (such as intermittent 404
	// model_not_found, 401 unauthenticated, or session invalidation): refresh token,
	// reset session, and retry once (matching Antigravity's durability).
	if !isMuseAuthOrSessionError(err) {
		return err
	}
	c.resetSession()
	fresh := c.refreshKey()
	if strings.TrimSpace(fresh) != "" {
		key = fresh
	}
	response, err = c.send(ctx, key, payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return decodeResponsesStream(c.info.Label, response.Body, emit)
}

// send posts a Responses request with the Muse identity headers.
func (c *museClient) send(ctx context.Context, key string, payload map[string]any) (*http.Response, error) {
	headers := map[string]string{
		"Authorization": "Bearer " + key,
		"User-Agent":    museUserAgent,
		"X-Client-Id":   "tbh:tui",
		"x-api-version": "1.0.0",
		"session_id":    c.session(),
	}
	return c.post(ctx, c.baseURL+"/responses", headers, payload)
}

// payload builds the Responses body shared by every Muse model.
func (c *museClient) payload(req ChatRequest) map[string]any {
	input := encodeResponsesInput(req)
	// Meta rejects a replayed function_call item that lacks an id and status,
	// answering with a misleading 404 model_not_found. The OpenAI Responses
	// feed these items came from always carries both, so restate them here.
	stampFunctionCallItems(input)
	instructions := strings.TrimSpace(req.System)
	var tools []map[string]any
	if len(req.Tools) > 0 {
		tools = encodeResponsesTools(req.Tools)
	}
	payload := map[string]any{
		"model":  req.Model,
		"stream": true,
		"input":  input,
	}
	if instructions != "" {
		payload["instructions"] = instructions
	}
	if len(tools) > 0 {
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
	}
	return payload
}

// isMuseAuthOrSessionError reports whether an error means the credential or session
// is the problem, matching Antigravity's isAuthOrSessionError. Meta returns 404
// model_not_found or 401 for an invalidated or aged session.
func isMuseAuthOrSessionError(err error) bool {
	if err == nil {
		return false
	}
	if museKeyStale(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "401") ||
		strings.Contains(msg, "unauthenticated") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "session") ||
		strings.Contains(msg, "token") ||
		strings.Contains(msg, "expired")
}

// stampFunctionCallItems adds the id and status Meta's Responses API expects on
// a function_call input item. Without them a request that replays tool history
// fails with 404 model_not_found, which reads as a bad model rather than a
// malformed item. The id only has to be present; a determinism from the call id
// keeps replays stable.
func stampFunctionCallItems(items []map[string]any) {
	for _, item := range items {
		if item["type"] != "function_call" {
			continue
		}
		if _, ok := item["id"]; !ok {
			callID, _ := item["call_id"].(string)
			if callID == "" {
				callID = "function"
			}
			item["id"] = "fc_" + callID
		}
		if _, ok := item["status"]; !ok {
			item["status"] = "completed"
		}
	}
}

// museKeyStale reports whether an error means the credential, not the request,
// is the problem. Meta returns 404 for an aged-out key, so it counts too.
func museKeyStale(err error) bool {
	var status *providerStatusError
	if !errors.As(err, &status) {
		return false
	}
	switch status.status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}
