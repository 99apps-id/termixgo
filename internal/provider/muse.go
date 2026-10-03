package provider

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

// museUserAgent is the identity Meta's Muse backend expects. It gates access,
// so the official CLI's build string is reused rather than the Termixgo agent.
const museUserAgent = "muse-build/1.3.0 (interactive; macos-aarch64; build ac7280f2aca67769d1455a8847bb502b617d50f6)"

// museRetryAttempts bounds how many times a transient Muse failure is retried
// with the same key before the key is treated as stale and re-minted. Meta is
// intermittently overloaded and answers a healthy request with 404
// model_not_found, so a retry costs far less than a needless re-mint.
const museRetryAttempts = 3

// museRetryWait is the pause before a retry. It is a variable so a test can
// remove the delay, and it carries jitter so parallel sessions do not retry in
// lockstep.
var museRetryWait = func(attempt int) time.Duration {
	delay := 500 * time.Millisecond << uint(attempt-1)
	if delay > 4*time.Second {
		delay = 4 * time.Second
	}
	return delay + time.Duration(rand.Int63n(int64(delay/2+1)))
}

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
	response, err := c.sendWithRetry(ctx, key, payload)
	if err == nil {
		defer response.Body.Close()
		return decodeResponsesStream(c.info.Label, response.Body, emit)
	}

	// Still failing after the transient retries means the credential or session,
	// not the network: refresh the token, reset the session, and try again once
	// (matching Antigravity's durability).
	if !isMuseAuthOrSessionError(err) {
		return err
	}
	c.resetSession()
	fresh := c.refreshKey()
	if strings.TrimSpace(fresh) != "" {
		key = fresh
	}
	response, err = c.sendWithRetry(ctx, key, payload)
	if err != nil {
		return museModelHint(req.Model, err)
	}
	defer response.Body.Close()
	return decodeResponsesStream(c.info.Label, response.Body, emit)
}

// museModelHint names the model a persistent failure belongs to. Meta answers
// a stale key, its own overload AND a model the account is not entitled to
// with the same 404 model_not_found, so when retries and a fresh key still
// fail the operator needs the model id and the tier to check, not the generic
// base-URL hint.
func museModelHint(model string, err error) error {
	var status *providerStatusError
	if !errors.As(err, &status) || status.status != http.StatusNotFound {
		return err
	}
	name := "the selected model"
	if trimmed := strings.TrimSpace(model); trimmed != "" {
		name = fmt.Sprintf("model %q", trimmed)
	}
	return fmt.Errorf("%w: this Muse login cannot reach %s — check that the Muse Code subscription is active and covers its tier (standard vs contributor), and that no custom base URL overrides the Muse endpoint", err, name)
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

// sendWithRetry posts, retrying a transient Muse failure with the same key a
// few times before giving up. Meta answers a request it cannot serve during an
// overload with 404 model_not_found, the same status it uses for a stale key,
// so the request is replayed rather than immediately re-minted.
func (c *museClient) sendWithRetry(ctx context.Context, key string, payload map[string]any) (*http.Response, error) {
	var err error
	for attempt := 1; attempt <= museRetryAttempts; attempt++ {
		var response *http.Response
		response, err = c.send(ctx, key, payload)
		if err == nil {
			return response, nil
		}
		if attempt == museRetryAttempts || !museTransient(err) {
			return nil, err
		}
		if waitErr := museWait(ctx, museRetryWait(attempt)); waitErr != nil {
			return nil, waitErr
		}
	}
	return nil, err
}

// museTransient reports whether an error is worth retrying with the same key.
// The 404 matters most: Meta uses it both for a stale key and for its own
// overload, so it is retried first and only treated as stale after that.
func museTransient(err error) bool {
	var status *providerStatusError
	if errors.As(err, &status) {
		switch status.status {
		case http.StatusNotFound,
			http.StatusRequestTimeout,
			http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	return retryableTransport(err)
}

// museWait pauses before a retry, honoring ctx.
func museWait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
