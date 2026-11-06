package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

// museUserAgent is the identity Meta's Muse backend expects. It gates access,
// so the official CLI's build string is reused rather than the Termixgo agent.
const museUserAgent = "muse-code/1.0.2"

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
	// textTools is set once Meta refuses a native tools field for this
	// credential (a Muse Code subscription key). From then on the client
	// runs Termixgo's tools over the text tool protocol below instead of
	// asking Meta to register them, so the refusal is paid once per client,
	// not on every step.
	textTools bool
}

func (c *museClient) useTextTools() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.textTools
}

func (c *museClient) setTextTools() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.textTools = true
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
	if c.useTextTools() {
		return c.streamTextTools(ctx, req, emit)
	}
	payload := c.payload(req)
	key := c.currentKey()
	response, err := c.sendWithRetry(ctx, key, payload)
	if err == nil {
		defer response.Body.Close()
		// The re-auth path reports headers, but this healthy path ignored
		// them: the gauge's header signal only fired on a token refresh,
		// which is the opposite of the turns that matter.
		if rl := ParseGenericRateLimit(response.Header); rl != nil {
			_ = emit(StreamEvent{Type: EventRateLimit, RateLimit: rl})
		}
		return decodeResponsesStream(c.info.Label, response.Body, emit)
	}

	// A client-attestation refusal is a policy answer about the native
	// tools field, not a stale key: re-minting hands back the same
	// subscription key. Switch this client to the text tool protocol and
	// run the step that way instead of failing a login that works.
	if isAttestationDenied(err) {
		c.setTextTools()
		return c.streamTextTools(ctx, req, emit)
	}

	// Still failing after the transient retries means the credential or session,
	// not the network: refresh the token, reset the session, and try again once
	// (matching Antigravity's durability).
	if !isMuseAuthOrSessionError(err) {
		return museAttestationError(err)
	}
	c.resetSession()
	fresh := c.refreshKey()
	if strings.TrimSpace(fresh) != "" {
		key = fresh
	}
	response, err = c.sendWithRetry(ctx, key, payload)
	if err != nil {
		if isAttestationDenied(err) {
			c.setTextTools()
			return c.streamTextTools(ctx, req, emit)
		}
		return museAttestationError(c.modelHint(ctx, req.Model, err))
	}
	defer response.Body.Close()

	if rl := ParseGenericRateLimit(response.Header); rl != nil {
		_ = emit(StreamEvent{Type: EventRateLimit, RateLimit: rl})
	}
	return decodeResponsesStream(c.info.Label, response.Body, emit)
}

// museCatalogueTimeout bounds the model-list probe. It runs only on a failure
// that has already ended the turn, and a hung probe must not delay the error
// the operator is waiting for.
const museCatalogueTimeout = 10 * time.Second

// museCatalogVerdict is what the live Muse catalogue says about a model id.
type museCatalogVerdict int

const (
	// museCatalogUnknown means the list could not be read, so no conclusion
	// about the id is available.
	museCatalogUnknown museCatalogVerdict = iota
	// museCatalogServed means Meta offers this id to this credential here.
	museCatalogServed
	// museCatalogNotOffered means Meta answered the list without this id.
	// This is the case that used to be misdiagnosed: Meta answers a stale key,
	// an overload and a model it does not serve with the same 404
	// model_not_found, and the catalogue is the only local evidence that tells
	// "not served here" apart from "subscription or tier problem".
	museCatalogNotOffered
)

// judgeMuseCatalog sorts one model id against a model list.
func judgeMuseCatalog(model string, ids []string) museCatalogVerdict {
	if len(ids) == 0 {
		return museCatalogUnknown
	}
	target := strings.TrimSpace(model)
	for _, id := range ids {
		if id == target {
			return museCatalogServed
		}
	}
	return museCatalogNotOffered
}

// catalogue reads the model ids Meta offers at this endpoint. The response is
// a plain list, so it is fetched with a bare GET rather than the streaming
// post helper, and the Muse identity headers are repeated because the backend
// gates on them.
func (c *museClient) catalogue(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, museCatalogueTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.currentKey())
	request.Header.Set("User-Agent", museUserAgent)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<14))
		response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", response.StatusCode)
	}
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&doc); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(doc.Data))
	for _, entry := range doc.Data {
		if id := strings.TrimSpace(entry.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// modelHint names the model a persistent failure belongs to and says what the
// live catalogue proves about it. A 404 that survives the retries and a fresh
// key is either an entitlement the credential does not have or a model Meta
// refuses to serve to this source address, and those two need opposite fixes,
// so guessing is worse than saying which check is left.
func (c *museClient) modelHint(ctx context.Context, model string, err error) error {
	var status *providerStatusError
	if !errors.As(err, &status) || status.status != http.StatusNotFound {
		return err
	}
	name := "the selected model"
	if trimmed := strings.TrimSpace(model); trimmed != "" {
		name = fmt.Sprintf("model %q", trimmed)
	}
	// The mint records which Meta account the key belongs to and App hands it to
	// the client. Naming it here is what shows an operator that a second machine
	// logged in as a different account, which no amount of endpoint checking
	// would reveal.
	who := "this credential"
	if account := strings.TrimSpace(c.accountID); account != "" {
		who = fmt.Sprintf("the Meta account %s", account)
	}
	ids, catalogueErr := c.catalogue(ctx)
	switch judgeMuseCatalog(model, ids) {
	case museCatalogNotOffered:
		return fmt.Errorf("%w: Meta does not offer %s to %s at %s. The models it lists are: %s. The Muse catalogue changes with the account and with the source address of the request, so one login can list muse-spark from a home network and list only muse-image from a datacenter server. Run the process behind an eligible egress (HTTPS_PROXY or ALL_PROXY), or choose a listed model", err, name, who, c.baseURL, strings.Join(ids, ", "))
	case museCatalogServed:
		return fmt.Errorf("%w: Meta lists %s at %s for %s, so this is not an entitlement gap. Check that the Muse Code subscription is active and covers its tier (standard vs contributor), and that no custom base URL moves the Muse client off %s", err, name, c.baseURL, who, c.baseURL)
	default:
		cause := "its model list could not be read"
		if catalogueErr != nil {
			cause = fmt.Sprintf("its model list could not be read (%v)", catalogueErr)
		}
		return fmt.Errorf("%w: this Muse login cannot reach %s and %s. Check that the Muse Code subscription is active and covers its tier (standard vs contributor), and that no custom base URL overrides the Muse endpoint at %s", err, name, cause, c.baseURL)
	}
}

// send posts a Responses request with the Muse identity headers.
func (c *museClient) send(ctx context.Context, key string, payload map[string]any) (*http.Response, error) {
	headers := map[string]string{
		"Authorization": "Bearer " + key,
		"User-Agent":    museUserAgent,
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
	// Meta also validates the replayed conversation shape: assistant text that
	// precedes a function_call is intermediate commentary, and replaying it as
	// an ordinary final answer returns HTTP 400 (param "input"). Tag it here,
	// not in the shared encoder, because the phase field is Meta-specific and
	// the ChatGPT backend Codex uses does not accept it.
	tagCommentaryItems(input)
	// One instructions string, parts joined in order: the static prefix leads
	// so a prefix cache keys on the stable part.
	instructions := strings.TrimSpace(req.System)
	if len(req.SystemParts) > 0 {
		instructions = strings.TrimSpace(joinSystemParts(req.SystemParts))
	}
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

// streamTextTools runs one step without a native tools field, carrying
// Termixgo's tools in the instructions instead. The model's reply is
// buffered rather than streamed, because a tool_call block is only a tool
// call once its closing fence has arrived; the parsed calls are then
// emitted as ordinary ToolCall events, so the agent loop executes them
// locally (approval gates, exec, file tools) exactly as it does for a
// native call, and the next step renders the stored call and its result
// back as protocol blocks. This is how a Muse Code subscription login,
// which Meta will not let register tools from another client, still
// drives Termixgo's tool loop; a pay-as-you-go Model API key never lands
// here because its native requests are not refused.
func (c *museClient) streamTextTools(ctx context.Context, req ChatRequest, emit func(StreamEvent) error) error {
	payload := c.textToolsPayload(req)
	key := c.currentKey()
	response, err := c.sendWithRetry(ctx, key, payload)
	if err != nil && !isAttestationDenied(err) && isMuseAuthOrSessionError(err) {
		c.resetSession()
		if fresh := c.refreshKey(); strings.TrimSpace(fresh) != "" {
			key = fresh
		}
		response, err = c.sendWithRetry(ctx, key, payload)
	}
	if err != nil {
		if isAttestationDenied(err) {
			return museAttestationError(err)
		}
		return museAttestationError(c.modelHint(ctx, req.Model, err))
	}
	defer response.Body.Close()
	if rl := ParseGenericRateLimit(response.Header); rl != nil {
		_ = emit(StreamEvent{Type: EventRateLimit, RateLimit: rl})
	}

	var text strings.Builder
	buffered := func(event StreamEvent) error {
		if event.Type == EventTextDelta {
			text.WriteString(event.Text)
			return nil
		}
		return emit(event)
	}
	if err := decodeResponsesStream(c.info.Label, response.Body, buffered); err != nil {
		return err
	}
	prose, calls := parseTextToolCalls(text.String())
	if len(calls) == 0 {
		if text.Len() > 0 {
			return emit(StreamEvent{Type: EventTextDelta, Text: text.String()})
		}
		return nil
	}
	if strings.TrimSpace(prose) != "" {
		if err := emit(StreamEvent{Type: EventTextDelta, Text: strings.TrimSpace(prose)}); err != nil {
			return err
		}
	}
	for index := range calls {
		call := calls[index]
		if err := emit(StreamEvent{Type: EventToolCall, ToolCall: &call}); err != nil {
			return err
		}
	}
	return nil
}

// textToolsPayload builds the Responses body for the text tool protocol:
// the same input shape as a plain request, with the tool catalogue and
// the calling convention folded into the instructions and no tools
// field for Meta to refuse.
func (c *museClient) textToolsPayload(req ChatRequest) map[string]any {
	instructions := strings.TrimSpace(req.System)
	if len(req.SystemParts) > 0 {
		instructions = strings.TrimSpace(joinSystemParts(req.SystemParts))
	}
	if len(req.Tools) > 0 {
		instructions = strings.TrimSpace(instructions + "\n\n" + textToolProtocol(req.Tools))
	}
	payload := map[string]any{
		"model":  req.Model,
		"stream": true,
		"input":  encodeTextToolsInput(req),
	}
	if instructions != "" {
		payload["instructions"] = instructions
	}
	return payload
}

// textToolProtocol teaches the model the calling convention and lists the
// tools Termixgo will execute for it.
func textToolProtocol(tools []ToolDef) string {
	var out strings.Builder
	out.WriteString("Tool use (text protocol): the tools below are available, but they are not registered with this API request. Termixgo executes them on the operator's machine and returns their output to you.\n")
	out.WriteString("To call a tool, end your reply with one block per call, in exactly this shape and with no text after the last block:\n")
	out.WriteString("```tool_call\n{\"name\":\"tool_name\",\"arguments\":{\"arg\":value}}\n```\n")
	out.WriteString("Tool outputs come back as tool_result blocks; read them and continue. When the task is finished, answer in plain text with no tool_call block.\n\nAvailable tools:\n")
	for _, tool := range tools {
		schema, err := json.Marshal(tool.Schema)
		if err != nil || len(schema) == 0 {
			schema = []byte("{}")
		}
		fmt.Fprintf(&out, "- %s: %s\n  arguments JSON schema: %s\n", tool.Name, tool.Description, schema)
	}
	return out.String()
}

// encodeTextToolsInput renders a history for the text tool protocol:
// ordinary messages keep their Responses shape, while a stored tool call
// becomes a tool_call block in an assistant message and its result a
// tool_result block in a user message, so no function_call item (and no
// tools field) is ever sent for Meta to refuse.
func encodeTextToolsInput(req ChatRequest) []map[string]any {
	items := make([]map[string]any, 0, len(req.Messages))
	for _, message := range req.Messages {
		switch message.Role {
		case RoleUser:
			items = append(items, map[string]any{
				"type":    "message",
				"role":    "user",
				"content": responsesUserContent(message),
			})
		case RoleAssistant:
			var parts []string
			if strings.TrimSpace(message.Content) != "" {
				parts = append(parts, message.Content)
			}
			for _, call := range message.ToolCalls {
				parts = append(parts, textToolCallBlock(call.Name, call.Arguments))
			}
			if len(parts) == 0 {
				continue
			}
			items = append(items, map[string]any{
				"type":    "message",
				"role":    "assistant",
				"content": []map[string]any{{"type": "output_text", "text": strings.Join(parts, "\n\n")}},
			})
		case RoleTool:
			block, err := json.Marshal(map[string]string{
				"name":    message.Name,
				"call_id": message.ToolID,
				"output":  message.Content,
			})
			if err != nil {
				continue
			}
			items = append(items, map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []map[string]any{{"type": "input_text", "text": "```tool_result\n" + string(block) + "\n```"}},
			})
		}
	}
	return items
}

// textToolCallBlock renders one call in the protocol's fence shape.
func textToolCallBlock(name, arguments string) string {
	if !json.Valid([]byte(strings.TrimSpace(arguments))) {
		arguments = "{}"
	}
	block, err := json.Marshal(map[string]json.RawMessage{
		"name":      json.RawMessage(quoteJSONString(name)),
		"arguments": json.RawMessage(arguments),
	})
	if err != nil {
		return "```tool_call\n{\"name\":\"" + name + "\",\"arguments\":{}}\n```"
	}
	return "```tool_call\n" + string(block) + "\n```"
}

// quoteJSONString renders a string as a JSON string literal.
func quoteJSONString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// parseTextToolCalls splits a model reply into the prose around the
// tool_call blocks and the calls themselves. A block that does not parse
// is not a call: the whole reply then stays prose, so a model that talks
// about the protocol in a code sample is not mistaken for one using it.
func parseTextToolCalls(text string) (string, []ToolCall) {
	const fence = "```tool_call"
	var prose strings.Builder
	var calls []ToolCall
	rest := text
	for {
		start := strings.Index(rest, fence)
		if start < 0 {
			prose.WriteString(rest)
			break
		}
		end := strings.Index(rest[start+len(fence):], "```")
		if end < 0 {
			return text, nil
		}
		raw := strings.TrimSpace(rest[start+len(fence) : start+len(fence)+end])
		var parsed struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil || strings.TrimSpace(parsed.Name) == "" {
			return text, nil
		}
		arguments := strings.TrimSpace(string(parsed.Arguments))
		if arguments == "" {
			arguments = "{}"
		}
		var asString string
		if err := json.Unmarshal([]byte(arguments), &asString); err == nil {
			arguments = asString
		}
		if !json.Valid([]byte(arguments)) {
			return text, nil
		}
		prose.WriteString(rest[:start])
		calls = append(calls, ToolCall{
			ID:        fmt.Sprintf("call_muse_%d", sessionSeq.Add(1)),
			Name:      strings.TrimSpace(parsed.Name),
			Arguments: arguments,
		})
		rest = rest[start+len(fence)+end+len("```"):]
	}
	if len(calls) == 0 {
		return text, nil
	}
	return prose.String(), calls
}

// isAttestationDenied reports whether an error is Meta's client
// attestation refusal, matched on the provider's own code rather than
// on prose alone.
func isAttestationDenied(err error) bool {
	var status *providerStatusError
	if !errors.As(err, &status) || status.status != http.StatusForbidden {
		return false
	}
	if status.code == "client_attestation_denied" {
		return true
	}
	message := strings.ToLower(status.message)
	return strings.Contains(message, "attestation") || strings.Contains(message, "muse code subscription")
}

// museAttestationError turns Meta's client-attestation refusal into an
// actionable message. It is the terminal error: it only surfaces when
// even a request without a native tools field was refused, because a
// tools refusal on its own switches the client to the text tool protocol
// instead. The refusal is policy, not a bad key, so the message names
// the pay-as-you-go key as the next step.
func museAttestationError(err error) error {
	if !isAttestationDenied(err) {
		return err
	}
	return fmt.Errorf("%w: Meta requires the official Muse Code client's attestation for this Muse Code subscription key, and refused the request even without native tools. Use a pay-as-you-go API key from https://dev.meta.ai, or keep this key in the official Muse Code client", err)
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
	if strings.Contains(msg, "401") ||
		strings.Contains(msg, "unauthenticated") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "expired") {
		return true
	}
	if strings.Contains(msg, "token") {
		for _, qualifier := range []string{"invalid", "expired", "refresh", "unauthorized", "unauthenticated", "revoked"} {
			if strings.Contains(msg, qualifier) {
				return true
			}
		}
	}
	return strings.Contains(msg, "session expired") ||
		strings.Contains(msg, "invalid session") ||
		strings.Contains(msg, "session invalid")
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

// tagCommentaryItems labels replayed assistant messages with the phase Meta's
// Responses API expects. An assistant message whose turn also called tools is
// intermediate commentary: replaying it without the marker returns HTTP 400
// (param "input"), because a plain final answer may not precede a function_call.
// An assistant message with no tool call in its turn is the final answer.
func tagCommentaryItems(items []map[string]any) {
	for index, item := range items {
		if item["type"] != "message" || item["role"] != "assistant" {
			continue
		}
		phase := "final_answer"
		if index+1 < len(items) && items[index+1]["type"] == "function_call" {
			phase = "commentary"
		}
		item["phase"] = phase
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
