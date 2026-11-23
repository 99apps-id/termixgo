package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// antigravityClient speaks Google's Cloud Code API behind an Antigravity OAuth
// login. It is not chat-completions or the Gemini REST surface: the request is
// a Cloud Code envelope and the stream is a wrapped Gemini candidate feed.
//
// The wire shapes follow the 9router reference (open-sse/executors/
// antigravity.js): one project id, a per-connection session id, and a
// streamGenerateContent call on the daily host.
type antigravityClient struct {
	*httpClient

	mu      sync.Mutex
	project string
	// signatures remembers a functionCall's thoughtSignature so it can be
	// replayed on the next request, which Gemini 3 requires for a tool loop.
	signatures map[string]string
	sessionID  string
	// cascade and trajectory are the conversation and execution ids the CLI
	// puts in requestId and in its labels, one pair per client.
	cascade    string
	trajectory string
}

// agConsumerProject is the project the CLI puts on every call of a consumer
// account that Cloud Code assigns none.
const agConsumerProject = "aicode-consumers"

const (
	// defaultAGCLIVersion and defaultAGChangelist are the pair the installed
	// Antigravity CLI reports (agy 1.5.15, read from its own request to a
	// local endpoint). The changelist is part of the identity: a version with
	// the wrong changelist is a client no release ever was.
	defaultAGCLIVersion = "1.5.15"
	defaultAGChangelist = "1027820220"
	agMaxOutputToken    = 65536
)

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

// agVersionOverride is a version the operator pinned by hand.
func agVersionOverride() string {
	return firstEnv("TERMIXGO_ANTIGRAVITY_CLI_VERSION", "ANTIGRAVITY_CLI_VERSION", "ODON_ANTIGRAVITY_CLI_VERSION")
}

// agCLIVersion is the version the requests present. Precedence: a pin from the
// environment, then the version of the CLI installed on this machine (so the
// identity follows the CLI when it updates itself), then the release this
// build was written against.
func agCLIVersion() string {
	if v := agVersionOverride(); v != "" {
		return v
	}
	if identity, ok := agyIdentityLookup(); ok {
		return identity.Version
	}
	return defaultAGCLIVersion
}

// agChangelist is the build changelist the CLI puts in its User-Agent. An
// overridden version drops the bundled changelist unless one is given too,
// because the pair is only valid together.
func agChangelist() string {
	if v := firstEnv("TERMIXGO_ANTIGRAVITY_CLI_CL"); v != "" {
		return v
	}
	if agVersionOverride() != "" {
		return ""
	}
	if identity, ok := agyIdentityLookup(); ok {
		return identity.Changelist
	}
	return defaultAGChangelist
}

// agUserAgent returns the Antigravity CLI identity, in the exact shape agy
// sends on every Cloud Code call:
//
//	antigravity/cli/<version> (aidev_client; os_type=<os>; arch=<arch>; cl=<changelist>; auth_method=consumer)
//
// Cloud Code lists Gemini 4 only for the CLI identity, and an account whose
// traffic does not look like agy's is the one that gets limited, so nothing
// here is invented: the OS and architecture are Go's own spelling, which is
// what the CLI reports, and every other field is the CLI's.
func agUserAgent() string {
	fields := []string{"aidev_client", "os_type=" + runtime.GOOS, "arch=" + runtime.GOARCH}
	if cl := agChangelist(); cl != "" {
		fields = append(fields, "cl="+cl)
	}
	method := firstEnv("TERMIXGO_ANTIGRAVITY_AUTH_METHOD")
	if method == "" {
		method = "consumer"
	}
	fields = append(fields, "auth_method="+method)
	return fmt.Sprintf("antigravity/cli/%s (%s)", agCLIVersion(), strings.Join(fields, "; "))
}

func gemini4Family(model string) string {
	id := strings.TrimPrefix(model, "antigravity-")
	rest, ok := strings.CutPrefix(id, "gemini-4-")
	if !ok {
		return ""
	}
	family := strings.SplitN(rest, "-", 2)[0]
	switch family {
	case "neon", "hydrogen", "argon", "carbon":
		return family
	default:
		return ""
	}
}

func gemini4Levels(family string) []string {
	if family == "hydrogen" || family == "neon" {
		return []string{"low", "medium", "high", "xhigh"}
	}
	return []string{"low", "medium", "high"}
}

// agSuffixLevels are the thinking levels the Antigravity backend offers as a
// model-name suffix for the non-Gemini-4 models the CLI lists: Gemini 3.x
// Flash and the Claude 5.5 pair take low, medium and high.
var agSuffixLevels = []string{"low", "medium", "high"}

// agSuffixedModel splits a wire model id into its base and the thinking
// level named by its suffix, for the families whose effort rides in the name.
// ok is false for any other id.
func agSuffixedModel(model string) (base, level string, ok bool) {
	id := strings.TrimPrefix(model, "antigravity-")
	for _, candidate := range agSuffixLevels {
		if base, found := strings.CutSuffix(id, "-"+candidate); found && base != "" {
			if strings.HasPrefix(base, "gemini-3") || strings.HasPrefix(base, "claude-") {
				return base, candidate, true
			}
		}
	}
	return "", "", false
}

// agModelEnums are the model_enum labels the CLI attaches per wire model,
// captured from its own requests. A model with no captured label is sent
// without one rather than with a guess.
var agModelEnums = map[string]string{
	"gemini-4-argon-low":      "MODEL_PLACEHOLDER_M419",
	"gemini-4-argon-medium":   "MODEL_PLACEHOLDER_M418",
	"gemini-4-argon-high":     "MODEL_PLACEHOLDER_M417",
	"gemini-3.8-flash-low":    "MODEL_PLACEHOLDER_M320",
	"gemini-3.8-flash-medium": "MODEL_PLACEHOLDER_M319",
	"gemini-3.8-flash-high":   "MODEL_PLACEHOLDER_M318",
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

// gemini4WireModel maps a model id and optional effort level to the wire id
// the Antigravity CLI backend expects: the thinking level is part of the model
// name, so the effort picks the suffix. A level the family lacks is lowered to
// its highest.
func gemini4WireModel(model, effort string) string {
	id := strings.TrimPrefix(model, "antigravity-")
	family := gemini4Family(id)
	if family == "" {
		return agSuffixedWireModel(model, effort)
	}
	levels := gemini4Levels(family)
	base := "gemini-4-" + family
	if effort == "" {
		if suffix, ok := strings.CutPrefix(id, base+"-"); ok && containsString(levels, suffix) {
			return id
		}
		if family == "hydrogen" {
			return base + "-high"
		}
		return base + "-medium"
	}
	wanted := "high"
	switch effort {
	case EffortLow:
		wanted = "low"
	case EffortMedium:
		wanted = "medium"
	case EffortHigh:
		wanted = "high"
	case EffortXHigh, EffortMax, EffortUltra:
		wanted = "xhigh"
	}
	if !containsString(levels, wanted) {
		wanted = levels[len(levels)-1]
	}
	return base + "-" + wanted
}

// agSuffixedWireModel applies an effort to a Gemini 3.x Flash or Claude wire id
// whose level is a name suffix. Any other id is returned unchanged, and so is
// one with no effort set, which keeps the level the catalogue entry names.
func agSuffixedWireModel(model, effort string) string {
	base, _, ok := agSuffixedModel(model)
	if !ok || effort == "" {
		return strings.TrimPrefix(model, "antigravity-")
	}
	return base + "-" + clampEffort(effort, agSuffixLevels)
}

func gemini4BareModel(wireModel string) string {
	family := gemini4Family(wireModel)
	if family == "" {
		return ""
	}
	bare := "gemini-4-" + family
	if bare == wireModel {
		return ""
	}
	return bare
}

func isGemini4ModelNotFound(err error) bool {
	if err == nil {
		return false
	}
	var statusErr *providerStatusError
	if errors.As(err, &statusErr) && statusErr.status != http.StatusBadRequest && statusErr.status != http.StatusNotFound {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "not found") ||
		strings.Contains(lower, "not_found") ||
		strings.Contains(lower, "not supported") ||
		strings.Contains(lower, "invalid")
}

// The onboarding endpoints are variables so tests can point them at a server.
var (
	agLoadAssistURL = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	agOnboardURL    = "https://cloudcode-pa.googleapis.com/v1internal:onboardUser"
)

func (c *antigravityClient) Stream(ctx context.Context, req ChatRequest, emit func(StreamEvent) error) error {
	project, err := c.ensureProject(ctx)
	if err != nil {
		return err
	}
	session := c.session()
	wireModel := gemini4WireModel(req.Model, req.Effort)
	steps := agModelSteps(req.Messages)
	body := map[string]any{
		"project":     project,
		"model":       wireModel,
		"userAgent":   "antigravity",
		"requestType": "agent",
		"requestId":   c.requestID(steps),
		"request": map[string]any{
			"contents":         c.encodeContents(req),
			"sessionId":        session,
			"generationConfig": agGenerationConfig(req, wireModel),
			"labels":           c.labels(wireModel, steps),
		},
	}
	request := body["request"].(map[string]any)
	if strings.TrimSpace(req.System) != "" {
		request["systemInstruction"] = map[string]any{
			"role":  "user",
			"parts": []map[string]any{{"text": req.System}},
		}
	}
	if len(req.Tools) > 0 {
		// The CLI sends no toolConfig, so neither does this client.
		request["tools"] = []map[string]any{{"functionDeclarations": agTools(req.Tools)}}
	}

	key := c.currentKey()
	// The CLI sends exactly four headers on a chat call: the token, the JSON
	// content type, its User-Agent and Go's gzip negotiation. Accept is
	// emptied because the shared poster would add an event-stream one, and no
	// session header is sent because the CLI has none.
	headers := map[string]string{
		"Authorization": "Bearer " + key,
		"User-Agent":    agUserAgent(),
		"Accept":        "",
	}
	endpoint := c.baseURL + "/v1internal:streamGenerateContent?alt=sse"
	response, err := c.post(ctx, endpoint, headers, body)
	if err != nil {
		if isAuthOrSessionError(err) {
			// Token expired or session invalidated on Google's backend: refresh token, reset session, and retry once.
			newKey := c.refreshKey()
			newSession := c.resetSession()
			headers["Authorization"] = "Bearer " + newKey
			reqMap := body["request"].(map[string]any)
			reqMap["sessionId"] = newSession
			body["requestId"] = c.requestID(steps)
			response, err = c.post(ctx, endpoint, headers, body)
		}
		if err != nil {
			if bareModel := gemini4BareModel(wireModel); bareModel != "" && isGemini4ModelNotFound(err) {
				body["model"] = bareModel
				body["requestId"] = c.requestID(steps)
				response, err = c.post(ctx, endpoint, headers, body)
			}
		}
		// The daily host is the chat host; when it fails at the endpoint level,
		// retry once on the prod host. The check must name the daily host,
		// because "cloudcode-pa.googleapis.com" is a substring of it too.
		if err != nil && isEndpointError(err) && strings.Contains(c.baseURL, "daily-cloudcode-pa") {
			fallbackURL := "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse"
			response, err = c.post(ctx, fallbackURL, headers, body)
		}
		if err != nil {
			return err
		}
	}
	defer response.Body.Close()

	if rl := ParseGenericRateLimit(response.Header); rl != nil {
		_ = emit(StreamEvent{Type: EventRateLimit, RateLimit: rl})
	}

	reader := newSSEReader(response.Body)
	usage := &cumulativeUsage{}
	emitted := false
	finish := ""
	for {
		payload, err := reader.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return wrapStreamError(c.info.Label, err)
		}
		if strings.TrimSpace(payload) == "" {
			continue
		}
		// Each event is {"response": {...}} on the daily host, but tolerate a
		// bare candidate object too.
		var envelope struct {
			Response json.RawMessage `json:"response"`
		}
		candidate := []byte(payload)
		if json.Unmarshal([]byte(payload), &envelope) == nil && len(envelope.Response) > 0 {
			candidate = envelope.Response
		}
		var chunk agResponse
		if json.Unmarshal(candidate, &chunk) != nil {
			continue
		}
		if chunk.UsageMetadata != nil {
			if step, ok := usage.step(Usage{
				PromptTokens:     chunk.UsageMetadata.PromptTokenCount,
				CompletionTokens: chunk.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      chunk.UsageMetadata.TotalTokenCount,
				CacheReadTokens:  chunk.UsageMetadata.CachedContentTokenCount,
				ReasoningTokens:  chunk.UsageMetadata.ThoughtsTokenCount,
			}); ok {
				if err := emit(StreamEvent{Type: EventUsage, Usage: &step}); err != nil {
					return err
				}
			}
		}
		if len(chunk.Candidates) == 0 {
			if chunk.PromptFeedback != nil && strings.TrimSpace(chunk.PromptFeedback.BlockReason) != "" {
				message := chunk.PromptFeedback.BlockReasonMessage
				if strings.TrimSpace(message) == "" {
					message = "the request was blocked before generation"
				}
				return fmt.Errorf("the endpoint blocked the request (%s): %s", chunk.PromptFeedback.BlockReason, message)
			}
			continue
		}
		if reason := strings.TrimSpace(chunk.Candidates[0].FinishReason); reason != "" {
			finish = reason
		}
		for _, part := range chunk.Candidates[0].Content.Parts {
			if part.FunctionCall != nil {
				emitted = true
				c.rememberSignature(part.FunctionCall.ID, part.ThoughtSignature)
				arguments, _ := json.Marshal(part.FunctionCall.Args)
				call := ToolCall{ID: part.FunctionCall.ID, Name: part.FunctionCall.Name, Arguments: string(arguments)}
				if err := emit(StreamEvent{Type: EventToolCall, ToolCall: &call}); err != nil {
					return err
				}
				continue
			}
			if part.Text == "" {
				continue
			}
			emitted = true
			kind := EventTextDelta
			if part.Thought {
				kind = EventReasoningDelta
			}
			if err := emit(StreamEvent{Type: kind, Text: part.Text}); err != nil {
				return err
			}
		}
	}
	// A candidate that ended without content for a reason other than a clean
	// stop (a safety block, a token cap, a malformed function call) is not a
	// step worth retrying blindly: the same request returns the same empty
	// answer. Surface the reason so the turn explains itself instead of
	// looping on a silent empty step.
	if !emitted && finish != "" && !strings.EqualFold(finish, "STOP") {
		return fmt.Errorf("the model ended the turn without an answer (finish reason %s)", strings.ToLower(finish))
	}
	return nil
}

// ensureProject resolves the Cloud Code project id, onboarding the account the
// first time. The id is cached for the life of the client.
func (c *antigravityClient) ensureProject(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.project != "" {
		return c.project, nil
	}
	if strings.TrimSpace(c.accountID) != "" {
		c.project = c.accountID
		return c.project, nil
	}
	// The CLI names only its IDE type here; the numeric platform and plugin
	// fields belong to the IDE client and are not part of its request.
	metadata := map[string]any{"ideType": "ANTIGRAVITY"}
	raw, err := c.doJSON(ctx, agLoadAssistURL, map[string]any{"metadata": metadata})
	if err != nil {
		return "", err
	}
	var loaded struct {
		Project     json.RawMessage `json:"cloudaicompanionProject"`
		CurrentTier *struct {
			ID string `json:"id"`
		} `json:"currentTier"`
		Tiers []struct {
			ID                 string `json:"id"`
			IsDefault          bool   `json:"isDefault"`
			UserDefinedProject bool   `json:"userDefinedCloudaicompanionProject"`
		} `json:"allowedTiers"`
	}
	_ = json.Unmarshal(raw, &loaded)
	project := agProjectID(loaded.Project)
	// A consumer account that is already enrolled answers with a tier and no
	// project. The CLI then names the shared consumer project, so there is
	// nothing to onboard and nothing to invent.
	if project == "" && loaded.CurrentTier != nil && strings.TrimSpace(loaded.CurrentTier.ID) != "" {
		project = agConsumerProject
	}
	tier := "legacy-tier"
	for _, candidate := range loaded.Tiers {
		if candidate.IsDefault {
			tier = candidate.ID
			break
		}
	}
	if project == "" && loaded.CurrentTier == nil {
		// A tier that wants the operator's own Cloud project (a workspace or
		// paid account) cannot be onboarded here: the shared consumer project is
		// refused for it, and every chat call would answer 403. Say so now.
		for _, candidate := range loaded.Tiers {
			if candidate.UserDefinedProject {
				return "", fmt.Errorf("this Google account answered as the %s tier, which needs its own Cloud project and is not supported here. A login made with another build or client can read this way: run 'termixgo logout antigravity' and then 'termixgo login antigravity'", candidate.ID)
			}
		}
	}
	if project == "" {
		for attempt := 0; attempt < 3 && project == ""; attempt++ {
			onboard, err := c.doJSON(ctx, agOnboardURL, map[string]any{"tierId": tier, "metadata": metadata})
			if err != nil {
				break
			}
			var parsed struct {
				Done     bool `json:"done"`
				Response struct {
					Project json.RawMessage `json:"cloudaicompanionProject"`
				} `json:"response"`
			}
			_ = json.Unmarshal(onboard, &parsed)
			project = agProjectID(parsed.Response.Project)
			if parsed.Done && project != "" {
				break
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	if project == "" {
		// The CLI sends the consumer project when the account names none; an
		// invented id is a project no real client ever used.
		project = agConsumerProject
	}
	c.project = project
	return project, nil
}

func (c *antigravityClient) doJSON(ctx context.Context, endpoint string, payload any) ([]byte, error) {
	key := c.currentKey()
	body, status, err := c.doJSONWithKey(ctx, endpoint, payload, key)
	if err == nil && status >= 200 && status < 300 {
		return body, nil
	}
	if status == http.StatusUnauthorized {
		freshKey := c.refreshKey()
		body, status, err = c.doJSONWithKey(ctx, endpoint, payload, freshKey)
		if err == nil && status >= 200 && status < 300 {
			return body, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%s returned %d: %s", endpoint, status, clipRunes(strings.TrimSpace(string(body)), 200))
}

func (c *antigravityClient) doJSONWithKey(ctx context.Context, endpoint string, payload any, key string) ([]byte, int, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	request, err := http.NewRequestWithContext(streamCtx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("User-Agent", agUserAgent())
	response, err := c.http.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	// The onboarding calls are one-shot JSON, but a hung connection after the
	// headers must still fail instead of holding the turn open.
	body, err := io.ReadAll(newStallGuard(response.Body, streamStallDelay, cancel))
	if err != nil {
		return nil, 0, err
	}
	if len(body) > 1<<20 {
		body = body[:1<<20]
	}
	return body, response.StatusCode, nil
}

func (c *antigravityClient) session() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessionID == "" {
		sum := sha256.Sum256([]byte(randomHex(16)))
		// Positive 63-bit integer string. Google Cloud Code requires a non-negative session ID.
		c.sessionID = fmt.Sprintf("%d", binary.BigEndian.Uint64(sum[:8])&0x7FFFFFFFFFFFFFFF)
	}
	return c.sessionID
}

func (c *antigravityClient) resetSession() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	sum := sha256.Sum256([]byte(randomHex(16)))
	c.sessionID = fmt.Sprintf("%d", binary.BigEndian.Uint64(sum[:8])&0x7FFFFFFFFFFFFFFF)
	c.signatures = map[string]string{}
	return c.sessionID
}

func (c *antigravityClient) rememberSignature(callID, signature string) {
	if callID == "" || signature == "" {
		return
	}
	c.mu.Lock()
	if c.signatures == nil {
		c.signatures = map[string]string{}
	}
	c.signatures[callID] = signature
	c.mu.Unlock()
}

func (c *antigravityClient) signatureFor(callID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.signatures[callID]
}

// encodeContents maps the shared message list to Gemini contents.
//
// The backend holds function calling to a stricter contract than the shared
// list this is built from: a model turn carrying a functionCall must be
// answered by the very next turn, and that turn has to be one user turn
// holding a functionResponse for every call the model turn made. A
// functionCall turn must itself come straight after a user turn or a function
// response turn. Google answers a history that breaks either rule with a 400
// "Please ensure that function call turn comes immediately after a user turn
// or after a function response turn."
//
// The shared list stores one message per tool result, so a step that called
// three tools arrives as three RoleTool messages. Emitting each as its own
// user turn left the second and third answers standing behind an answer the
// backend had already consumed, which is the shape it refuses: consecutive
// results are folded into the single user turn that answers the call. A turn
// that would encode to no parts at all keeps a placeholder part instead of
// vanishing, because dropping a user turn from between two model turns breaks
// the same rule from the other side.
func (c *antigravityClient) encodeContents(req ChatRequest) []map[string]any {
	contents := make([]map[string]any, 0, len(req.Messages))
	// appendUser places a user turn, folding it into the one already at the
	// end when there is one. A tool that also produced an image adds a user
	// turn right after its results, and an image turn split from the answers
	// it belongs to leaves the same dangling functionCall behind.
	appendUser := func(parts []map[string]any) {
		if len(parts) == 0 {
			parts = []map[string]any{{"text": "(empty)"}}
		}
		if len(contents) > 0 && contents[len(contents)-1]["role"] == "user" {
			// If the previous user turn is a function response and this turn is regular user content
			// (or vice versa), Gemini strictly forbids consecutive user turns. Every function response
			// must be followed by a model turn before the user can speak again. Two consecutive user
			// turns (function response followed by user text) trigger Google 400 INVALID_ARGUMENT.
			if antigravityHasFunctionResponse(contents[len(contents)-1]) != antigravityPartsHaveFunctionResponse(parts) {
				contents = append(contents, map[string]any{
					"role":  "model",
					"parts": []map[string]any{{"text": "(acknowledged)"}},
				})
				contents = append(contents, map[string]any{"role": "user", "parts": parts})
				return
			}
			if previous, ok := contents[len(contents)-1]["parts"].([]map[string]any); ok {
				contents[len(contents)-1]["parts"] = append(previous, parts...)
				return
			}
		}
		contents = append(contents, map[string]any{"role": "user", "parts": parts})
	}
	for index := 0; index < len(req.Messages); index++ {
		message := req.Messages[index]
		switch message.Role {
		case RoleUser:
			appendUser(antigravityUserParts(message))
		case RoleAssistant:
			parts := make([]map[string]any, 0, len(message.ToolCalls)+1)
			if strings.TrimSpace(message.Content) != "" {
				parts = append(parts, map[string]any{"text": message.Content})
			}
			for _, call := range message.ToolCalls {
				args, err := decodeArguments(call.Arguments)
				if err != nil {
					args = map[string]any{}
				}
				part := map[string]any{"functionCall": map[string]any{"id": call.ID, "name": call.Name, "args": args}}
				if signature := c.signatureFor(call.ID); signature != "" {
					part["thoughtSignature"] = signature
				}
				parts = append(parts, part)
			}
			if len(parts) == 0 {
				parts = append(parts, map[string]any{"text": "(acknowledged)"})
			}
			// If previous turn was also a model turn, fold parts together so we don't
			// create consecutive model turns or inject artificial user continuation turns.
			if len(contents) > 0 && contents[len(contents)-1]["role"] == "model" {
				if prevParts, ok := contents[len(contents)-1]["parts"].([]map[string]any); ok {
					contents[len(contents)-1]["parts"] = append(prevParts, parts...)
					continue
				}
			}
			contents = append(contents, map[string]any{"role": "model", "parts": parts})
		case RoleTool:
			// Every result a step produced belongs to the one user turn that
			// answers it, so the whole run of them is collected before that
			// turn is placed.
			parts := []map[string]any{antigravityFunctionResponse(message)}
			for index+1 < len(req.Messages) && req.Messages[index+1].Role == RoleTool {
				index++
				parts = append(parts, antigravityFunctionResponse(req.Messages[index]))
			}
			appendUser(parts)
		}
	}
	return antigravityAnchorFunctionCalls(contents)
}

// antigravityAnchorFunctionCalls repairs the other half of the rule: a model
// turn carrying a functionCall has to come straight after a user turn or a
// function response turn, so it may not stand first or follow another model
// turn. History can produce exactly that. Compact refuses to leave a tool
// result at the head, so a drop may take the user turn that opened the turn and
// leave the assistant's call standing first, and the loader only closes off the
// calls of the last step. The call and its answer still carry what the model
// did, so a continuation turn goes in front of the pair instead of dropping it:
// a 400 here ends the conversation, a turn that says nothing costs one step.
func antigravityAnchorFunctionCalls(contents []map[string]any) []map[string]any {
	if len(contents) == 0 {
		return []map[string]any{{"role": "user", "parts": []map[string]any{{"text": "(empty)"}}}}
	}

	// Gemini conversation history MUST begin with a user turn.
	if contents[0]["role"] != "user" {
		contents = append([]map[string]any{{"role": "user", "parts": []map[string]any{{"text": "(continued)"}}}}, contents...)
	}

	for index := 0; index < len(contents); index++ {
		// Consecutive model turns violate Gemini alternation; fold into previous model turn.
		if index > 0 && contents[index]["role"] == "model" && contents[index-1]["role"] == "model" {
			prevParts, ok1 := contents[index-1]["parts"].([]map[string]any)
			curParts, ok2 := contents[index]["parts"].([]map[string]any)
			if ok1 && ok2 {
				contents[index-1]["parts"] = append(prevParts, curParts...)
				contents = append(contents[:index], contents[index+1:]...)
				index--
				continue
			}
		}

		if !antigravityHasFunctionCall(contents[index]) {
			continue
		}
		// A functionCall must follow a user turn or a function response turn,
		// never stand first or follow another model turn.
		if index == 0 || contents[index-1]["role"] != "user" {
			repaired := make([]map[string]any, 0, len(contents)+1)
			repaired = append(repaired, contents[:index]...)
			repaired = append(repaired, map[string]any{"role": "user", "parts": []map[string]any{{"text": "(continued)"}}})
			repaired = append(repaired, contents[index:]...)
			contents = repaired
			index++
		}

		// A functionCall must also be answered by the immediately following turn,
		// and that turn must contain a functionResponse for the call.
		// If the call is followed by another model turn, or a plain user text turn, or is the last turn:
		// synthesize the missing functionResponse so Google Cloud Code's invariant is satisfied.
		hasAnswer := false
		if index+1 < len(contents) && contents[index+1]["role"] == "user" {
			if antigravityHasFunctionResponse(contents[index+1]) {
				hasAnswer = true
			}
		}
		if !hasAnswer {
			parts, _ := contents[index]["parts"].([]map[string]any)
			var respParts []map[string]any
			for _, part := range parts {
				if fc, ok := part["functionCall"].(map[string]any); ok {
					id, _ := fc["id"].(string)
					name, _ := fc["name"].(string)
					respParts = append(respParts, map[string]any{
						"functionResponse": map[string]any{
							"id":       id,
							"name":     name,
							"response": map[string]any{"result": "(interrupted)"},
						},
					})
				}
			}
			if len(respParts) > 0 {
				repaired := make([]map[string]any, 0, len(contents)+2)
				repaired = append(repaired, contents[:index+1]...)
				repaired = append(repaired, map[string]any{"role": "user", "parts": respParts})
				if index+1 < len(contents) && contents[index+1]["role"] == "user" {
					repaired = append(repaired, map[string]any{"role": "model", "parts": []map[string]any{{"text": "(acknowledged)"}}})
				}
				repaired = append(repaired, contents[index+1:]...)
				contents = repaired
				index++
			}
		}
	}

	// Final pass: ensure strict Gemini alternation across all turns.
	var alternated []map[string]any
	for _, turn := range contents {
		if len(alternated) > 0 {
			lastRole, _ := alternated[len(alternated)-1]["role"].(string)
			curRole, _ := turn["role"].(string)
			if lastRole == curRole {
				if curRole == "user" {
					alternated = append(alternated, map[string]any{
						"role":  "model",
						"parts": []map[string]any{{"text": "(acknowledged)"}},
					})
				} else {
					alternated = append(alternated, map[string]any{
						"role":  "user",
						"parts": []map[string]any{{"text": "(continued)"}},
					})
				}
			}
		}
		alternated = append(alternated, turn)
	}
	return alternated
}

func antigravityHasFunctionCall(turn map[string]any) bool {
	parts, _ := turn["parts"].([]map[string]any)
	for _, part := range parts {
		if _, ok := part["functionCall"]; ok {
			return true
		}
	}
	return false
}

func antigravityHasFunctionResponse(turn map[string]any) bool {
	parts, _ := turn["parts"].([]map[string]any)
	return antigravityPartsHaveFunctionResponse(parts)
}

func antigravityPartsHaveFunctionResponse(parts []map[string]any) bool {
	for _, part := range parts {
		if _, ok := part["functionResponse"]; ok {
			return true
		}
	}
	return false
}

func antigravityUserParts(message Message) []map[string]any {
	parts := make([]map[string]any, 0, len(message.Images)+1)
	if strings.TrimSpace(message.Content) != "" {
		parts = append(parts, map[string]any{"text": message.Content})
	}
	for _, image := range message.Images {
		parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": image.MediaType, "data": image.Data}})
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{"text": "(empty)"})
	}
	return parts
}

// antigravityFunctionResponse answers one tool call. An empty result is
// replaced because the backend reads a blank response as a call that was never
// answered, which is the same rejection as a missing turn.
func antigravityFunctionResponse(message Message) map[string]any {
	content := message.Content
	if strings.TrimSpace(content) == "" {
		content = "(no output)"
	}
	return map[string]any{"functionResponse": map[string]any{
		"id":       message.ToolID,
		"name":     message.Name,
		"response": map[string]any{"result": content},
	}}
}

func agGenerationConfig(req ChatRequest, wireModel string) map[string]any {
	config := map[string]any{}
	if req.Temperature != nil {
		config["temperature"] = *req.Temperature
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 || maxTokens > agMaxOutputToken {
		maxTokens = agMaxOutputToken
	}
	config["maxOutputTokens"] = maxTokens
	// Gemini 3 models cannot disable thinking; ask for a thought summary so the
	// transcript shows the reasoning rather than dropping it.
	// The level follows the wire model's own suffix, which is what the CLI
	// does: the model name and the level it sends always agree. A model with
	// no suffix falls back to the requested effort, then to the balanced one.
	level := ""
	if _, suffix, ok := agSuffixedModel(wireModel); ok {
		level = suffix
	} else if family := gemini4Family(wireModel); family != "" {
		for _, candidate := range gemini4Levels(family) {
			if strings.HasSuffix(wireModel, "-"+candidate) {
				level = candidate
			}
		}
	}
	if level == "" {
		level = googleEffort(req.Effort)
	}
	if level == "" {
		level = "medium"
	}
	if level == "xhigh" {
		// The public thinking enum stops at HIGH; the xhigh tier is selected
		// by the model name.
		level = "high"
	}
	config["thinkingConfig"] = map[string]any{"thinkingLevel": strings.ToUpper(level), "includeThoughts": true}
	return config
}

func agTools(tools []ToolDef) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		schema := cleanAntigravitySchema(tool.Schema)
		out = append(out, map[string]any{
			"name":        sanitizeFunctionName(tool.Name),
			"description": tool.Description,
			"parameters":  upperSchemaTypes(schema),
		})
	}
	return out
}

// upperSchemaTypes writes every "type" in a function schema in the upper-case
// spelling the CLI sends (OBJECT, STRING, INTEGER). The backend reads both, so
// this is about sending what the CLI sends, not about accepting more. Inside
// "properties" the keys are property names, so only their values are walked.
func upperSchemaTypes(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, entry := range typed {
			switch {
			case key == "type":
				if name, ok := entry.(string); ok {
					out[key] = strings.ToUpper(name)
					continue
				}
				out[key] = entry
			case key == "properties":
				if properties, ok := entry.(map[string]any); ok {
					walked := make(map[string]any, len(properties))
					for name, property := range properties {
						walked[name] = upperSchemaTypes(property)
					}
					out[key] = walked
					continue
				}
				out[key] = upperSchemaTypes(entry)
			default:
				out[key] = upperSchemaTypes(entry)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, entry := range typed {
			out[index] = upperSchemaTypes(entry)
		}
		return out
	default:
		return value
	}
}

// cleanAntigravitySchema strips JSON Schema keywords Gemini rejects and makes
// sure the object carries a type and, for an empty object, a reason field.
func cleanAntigravitySchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{"reason": map[string]any{"type": "string"}}, "required": []string{"reason"}}
	}
	cleaned := stripSchemaKeys(schema).(map[string]any)
	if cleaned["type"] == nil {
		cleaned["type"] = "object"
	}
	properties, _ := cleaned["properties"].(map[string]any)
	if cleaned["type"] == "object" && len(properties) == 0 {
		cleaned["properties"] = map[string]any{"reason": map[string]any{"type": "string"}}
		cleaned["required"] = []string{"reason"}
	}
	// A required name without a matching property is rejected with
	// "property is not defined", which MCP servers emit.
	// normalizeAntigravityEnums runs first so a numeric enum never reaches
	// the wire: the Gemini function schema declares enum as repeated string
	// and answers a numeric entry with a 400 "Invalid value (TYPE_STRING)".
	return pruneUndefinedRequired(normalizeAntigravityEnums(cleaned)).(map[string]any)
}

// geminiUnsupportedKeys are JSON Schema keywords the Gemini function-declaration
// schema rejects with an "Unknown name" 400. MCP servers emit schemas with
// keywords like propertyNames, so every path that sends tools to Gemini must
// drop them. anyOf/oneOf/allOf/not are listed because Gemini cannot express
// them in a function schema.
var geminiUnsupportedKeys = map[string]bool{
	"$schema": true, "$id": true, "$ref": true, "$defs": true, "$comment": true,
	"definitions": true, "additionalProperties": true, "propertyNames": true,
	"patternProperties": true, "unevaluatedProperties": true, "unevaluatedItems": true,
	"dependentRequired": true, "dependentSchemas": true,
	"default": true, "examples": true, "title": true, "optional": true,
	"deprecated": true, "readOnly": true, "writeOnly": true,
	"uniqueItems": true, "const": true, "oneOf": true, "anyOf": true, "allOf": true, "not": true,
	"if": true, "then": true, "else": true,
	"exclusiveMinimum": true, "exclusiveMaximum": true, "minimum": true, "maximum": true,
	"multipleOf": true, "minLength": true, "maxLength": true, "pattern": true,
	"minItems": true, "maxItems": true, "minProperties": true, "maxProperties": true,
	"contentEncoding": true, "contentMediaType": true, "contentSchema": true,
}

var antigravityUnsupportedKeys = geminiUnsupportedKeys

func stripSchemaKeys(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, entry := range typed {
			if antigravityUnsupportedKeys[key] || strings.HasPrefix(key, "x-") {
				continue
			}
			// Inside "properties" the keys are property names, not schema
			// keywords. A tool is free to name a property "title" or "required";
			// filtering those names deleted the property and then left a
			// dangling required entry, which Gemini rejects.
			if key == "properties" {
				if properties, ok := entry.(map[string]any); ok {
					cleaned := make(map[string]any, len(properties))
					for name, property := range properties {
						cleaned[name] = stripSchemaKeys(property)
					}
					out[key] = cleaned
					continue
				}
			}
			// JSON Schema allows a type union, for example ["string","null"],
			// but the Gemini Schema proto wants one type string plus nullable.
			// A list there answers "Proto field is not repeating".
			if key == "type" {
				if chosen, nullable, ok := reduceTypeList(entry); ok {
					out["type"] = chosen
					if nullable {
						out["nullable"] = true
					}
					continue
				}
			}
			out[key] = stripSchemaKeys(entry)
		}
		if t, ok := out["type"].(string); ok {
			if t != "array" {
				delete(out, "items")
			} else {
				if items, ok := out["items"].(map[string]any); !ok || len(items) == 0 {
					out["items"] = map[string]any{"type": "string"}
				}
			}
			if t != "object" {
				delete(out, "properties")
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, entry := range typed {
			out = append(out, stripSchemaKeys(entry))
		}
		return out
	default:
		return value
	}
}

// normalizeAntigravityEnums stringifies every non-string enum entry, because
// the Gemini function schema declares enum as repeated string: a numeric enum
// such as [90, 180, 270] is rejected with a 400 "Invalid value (TYPE_STRING)".
// A node whose type was number or integer becomes string so the enum still
// matches its type. One bad MCP tool must not fail the whole turn.
func normalizeAntigravityEnums(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, entry := range typed {
			out[key] = normalizeAntigravityEnums(entry)
		}
		raw, ok := out["enum"]
		if !ok {
			return out
		}
		var entries []any
		switch list := raw.(type) {
		case []any:
			entries = list
		case []string:
			return out
		default:
			return out
		}
		stringified := make([]string, 0, len(entries))
		for _, item := range entries {
			if text, ok := antigravityEnumText(item); ok {
				stringified = append(stringified, text)
			}
		}
		if len(stringified) == 0 {
			delete(out, "enum")
			return out
		}
		out["enum"] = stringified
		if kind, _ := out["type"].(string); kind == "number" || kind == "integer" {
			out["type"] = "string"
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, entry := range typed {
			out = append(out, normalizeAntigravityEnums(entry))
		}
		return out
	default:
		return value
	}
}

// antigravityEnumText renders one enum entry as the string the Gemini schema
// accepts. ok is false for entries with no scalar form, which are dropped.
func antigravityEnumText(item any) (text string, ok bool) {
	switch value := item.(type) {
	case string:
		return value, true
	case bool:
		if value {
			return "true", true
		}
		return "false", true
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(value), 'f', -1, 32), true
	case int:
		return strconv.Itoa(value), true
	default:
		return "", false
	}
}

// pruneUndefinedRequired removes every name from a "required" list that has no
// sibling property. The Gemini function schema validates required against
// properties and answers "property is not defined" otherwise, which both
// hand-written and MCP-supplied schemas trip over.
func pruneUndefinedRequired(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, entry := range typed {
			out[key] = pruneUndefinedRequired(entry)
		}
		if required, ok := out["required"]; ok {
			properties, _ := out["properties"].(map[string]any)
			if filtered, keep := definedRequired(required, properties); keep {
				out["required"] = filtered
			} else {
				delete(out, "required")
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, entry := range typed {
			out[index] = pruneUndefinedRequired(entry)
		}
		return out
	default:
		return value
	}
}

// definedRequired keeps the required names that name a real property, reporting
// whether any survived.
func definedRequired(required any, properties map[string]any) (any, bool) {
	switch names := required.(type) {
	case []string:
		kept := make([]string, 0, len(names))
		for _, name := range names {
			if _, present := properties[name]; present {
				kept = append(kept, name)
			}
		}
		if len(kept) == 0 {
			return nil, false
		}
		return kept, true
	case []any:
		kept := make([]any, 0, len(names))
		for _, entry := range names {
			if text, ok := entry.(string); ok {
				if _, present := properties[text]; present {
					kept = append(kept, text)
				}
			}
		}
		if len(kept) == 0 {
			return nil, false
		}
		return kept, true
	default:
		return nil, false
	}
}

// reduceTypeList collapses a JSON Schema type union to a single Gemini type,
// reporting whether "null" was one of the members so the caller can set
// nullable. ok is false when the value is not a list, which means it already is
// a plain type string.
func reduceTypeList(value any) (chosen string, nullable bool, ok bool) {
	var names []string
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if name, isString := item.(string); isString {
				names = append(names, name)
			}
		}
	case []string:
		names = typed
	default:
		return "", false, false
	}
	if len(names) == 0 {
		return "", false, false
	}
	for _, name := range names {
		if strings.EqualFold(name, "null") {
			nullable = true
			continue
		}
		if chosen == "" {
			chosen = name
		}
	}
	if chosen == "" {
		chosen = "string"
	}
	return chosen, nullable, true
}

func sanitizeFunctionName(name string) string {
	var builder strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == ':', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	clean := builder.String()
	if clean == "" {
		return "_unknown"
	}
	if clean[0] >= '0' && clean[0] <= '9' {
		clean = "_" + clean
	}
	if len(clean) > 64 {
		clean = clean[:64]
	}
	return clean
}

func agProjectID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		trimmed := strings.TrimSpace(text)
		return strings.TrimPrefix(trimmed, "projects/")
	}
	var object struct {
		ID        string `json:"id"`
		ProjectID string `json:"projectId"`
		Project   string `json:"project"`
		Name      string `json:"name"`
	}
	if json.Unmarshal(raw, &object) == nil {
		for _, val := range []string{object.ID, object.ProjectID, object.Project, object.Name} {
			trimmed := strings.TrimSpace(val)
			if trimmed != "" {
				return strings.TrimPrefix(trimmed, "projects/")
			}
		}
	}
	return ""
}

func isAuthOrSessionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "401") ||
		strings.Contains(msg, "unauthenticated") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "expired") {
		return true
	}
	// A bare "token" match misfires on quota wording such as "token limit",
	// so a token mention only counts beside an auth qualifier.
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

func isEndpointError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "404") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host")
}

// agModelSteps counts the model turns already in the history, which is the
// step the CLI reports as last_step_index.
func agModelSteps(messages []Message) int {
	steps := 0
	for _, message := range messages {
		if message.Role == RoleAssistant {
			steps++
		}
	}
	return steps
}

// ids returns the conversation and execution UUIDs, created on first use and
// kept for the life of the client so one conversation reads as one.
func (c *antigravityClient) ids() (cascade, trajectory string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cascade == "" {
		c.cascade = randomUUID()
	}
	if c.trajectory == "" {
		c.trajectory = randomUUID()
	}
	return c.cascade, c.trajectory
}

// requestID is the CLI's request id: agent/<conversation>/<unix ms>/<execution>/<step>.
func (c *antigravityClient) requestID(steps int) string {
	cascade, trajectory := c.ids()
	return fmt.Sprintf("agent/%s/%d/%s/%d", cascade, time.Now().UnixMilli(), trajectory, steps+1)
}

// labels are the request labels the CLI attaches, with the same keys and the
// same string values.
func (c *antigravityClient) labels(wireModel string, steps int) map[string]string {
	cascade, trajectory := c.ids()
	claude := strings.HasPrefix(wireModel, "claude-")
	labels := map[string]string{
		"cascade_id":               cascade,
		"last_step_index":          strconv.Itoa(steps),
		"request_id":               fmt.Sprintf("%s-%d", trajectory, steps),
		"root_cascade_id":          cascade,
		"trajectory_id":            trajectory,
		"used_claude":              strconv.FormatBool(claude),
		"used_claude_conservative": "false",
		"used_non_gemini_model":    strconv.FormatBool(!strings.HasPrefix(wireModel, "gemini-")),
	}
	if enum, ok := agModelEnums[wireModel]; ok {
		labels["model_enum"] = enum
	}
	return labels
}

// randomUUID is a random version 4 UUID.
func randomUUID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

func randomHex(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(buffer)
}

type agResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text             string `json:"text"`
				Thought          bool   `json:"thought"`
				ThoughtSignature string `json:"thoughtSignature"`
				FunctionCall     *struct {
					ID   string         `json:"id"`
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		TotalTokenCount         int `json:"totalTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	// PromptFeedback is the request-level safety block. It arrives with no
	// candidates, which would otherwise read as a completed but empty step.
	PromptFeedback *struct {
		BlockReason        string `json:"blockReason"`
		BlockReasonMessage string `json:"blockReasonMessage"`
	} `json:"promptFeedback"`
}
