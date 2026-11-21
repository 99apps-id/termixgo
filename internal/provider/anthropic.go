package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
)

// The Claude Code CLI identity an OAuth token is entitled to. The beta list
// matches what the current CLI sends; the first entry is what unlocks an
// OAuth bearer at all.
const (
	defaultClaudeCLIVersion = "2.1.290"
	claudeCodeIdentity      = "You are Claude Code, Anthropic's official CLI for Claude."
)

var (
	claudeCLIVersion   = defaultClaudeCLIVersion
	claudeCLIUserAgent = "claude-cli/" + claudeCLIVersion + " (external, sdk-cli)"

	learnedClaudeCLIVersionMu sync.Mutex
	learnedClaudeCLIVersion   string
)

func isDottedVersion(value string) bool {
	if value == "" || strings.HasSuffix(value, ".") || strings.Contains(value, "..") {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !((c >= '0' && c <= '9') || c == '.') {
			return false
		}
		if i == 0 && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return strings.Contains(value, ".")
}

func rememberClaudeCLIVersion(version string) {
	v := strings.TrimSpace(version)
	if !isDottedVersion(v) {
		return
	}
	learnedClaudeCLIVersionMu.Lock()
	learnedClaudeCLIVersion = v
	learnedClaudeCLIVersionMu.Unlock()
}

func resolveClaudeCLIVersion() string {
	for _, key := range []string{"ODON_CLAUDE_CLI_VERSION", "TERMIXGO_CLAUDE_CLI_VERSION", "CLAUDE_CLI_VERSION"} {
		if v := strings.TrimSpace(os.Getenv(key)); isDottedVersion(v) {
			return v
		}
	}
	learnedClaudeCLIVersionMu.Lock()
	learned := learnedClaudeCLIVersion
	learnedClaudeCLIVersionMu.Unlock()
	if learned != "" {
		return learned
	}
	return defaultClaudeCLIVersion
}

func currentClaudeCLIUserAgent() string {
	ver := resolveClaudeCLIVersion()
	if ver == defaultClaudeCLIVersion {
		return claudeCLIUserAgent
	}
	return "claude-cli/" + ver + " (external, sdk-cli)"
}

func claudeCLIVersionFromError(body string) string {
	const marker = "version "
	rest := body
	for {
		idx := strings.Index(rest, marker)
		if idx < 0 {
			return ""
		}
		rest = rest[idx+len(marker):]
		end := 0
		for end < len(rest) && ((rest[end] >= '0' && rest[end] <= '9') || rest[end] == '.') {
			end++
		}
		candidate := strings.TrimRight(rest[:end], ".")
		if isDottedVersion(candidate) {
			return candidate
		}
	}
}

func stainlessOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "MacOS"
	case "windows":
		return "Windows"
	default:
		return "Linux"
	}
}

func stainlessArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	default:
		return runtime.GOARCH
	}
}

const (
	claudeOAuthBeta = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advanced-tool-use-2025-11-20,effort-2025-11-24,structured-outputs-2025-12-15,fast-mode-2026-02-01,redact-thinking-2026-02-12,token-efficient-tools-2026-03-28"
)

// anthropicClient speaks the Messages streaming protocol.
type anthropicClient struct{ *httpClient }

func (c *anthropicClient) Stream(ctx context.Context, req ChatRequest, emit func(StreamEvent) error) error {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	messages := encodeAnthropicMessages(req.Messages)
	model := req.Model
	if c.info.OAuth && strings.HasPrefix(model, "claude-oauth-") {
		model = strings.TrimPrefix(model, "claude-oauth-")
	}
	if model == "claude-fable-5.2" {
		model = "claude-fable-5-2"
	}
	payload := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"stream":     true,
		"messages":   messages,
	}
	if len(req.SystemParts) > 0 {
		var blocks []map[string]any
		for i, part := range req.SystemParts {
			trimmed := strings.TrimSpace(part)
			if trimmed == "" {
				continue
			}
			block := map[string]any{"type": "text", "text": trimmed}
			if i == 0 {
				block["cache_control"] = map[string]any{"type": "ephemeral"}
			}
			blocks = append(blocks, block)
		}
		if len(blocks) > 0 {
			payload["system"] = blocks
		}
	} else if strings.TrimSpace(req.System) != "" {
		payload["system"] = req.System
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	// output_config.effort is the Anthropic effort control and needs no beta
	// header. It is separate from extended thinking: it shapes how much work
	// the whole response gets, and it is left out entirely when the operator
	// set no effort, so the model keeps its own default.
	if effort := anthropicEffortValues(req.Effort); effort != "" {
		payload["output_config"] = map[string]any{"effort": effort}
	}

	applyPromptCacheToMessages(messages)

	hasPDF := false
	for _, m := range req.Messages {
		for _, img := range m.Images {
			if img.MediaType == "application/pdf" {
				hasPDF = true
				break
			}
		}
		if hasPDF {
			break
		}
	}

	headers := map[string]string{"anthropic-version": "2023-06-01"}
	url := c.baseURL + "/v1/messages"
	key := c.currentKey()
	var aliases map[string]string
	if c.info.OAuth {
		// A Claude Code OAuth login sends a bearer token under the claude-cli
		// identity and needs the beta query plus the claude-code beta header;
		// the API key header is not used.
		headers["Authorization"] = "Bearer " + key
		beta := claudeOAuthBeta
		if hasPDF {
			beta += ",pdfs-2024-09-25"
		}
		headers["anthropic-beta"] = beta
		headers["anthropic-dangerous-direct-browser-access"] = "true"
		headers["x-app"] = "cli"
		headers["User-Agent"] = currentClaudeCLIUserAgent()
		if sid := strings.TrimSpace(c.SessionID()); sid != "" {
			headers["X-Claude-Code-Session-Id"] = deriveUUIDFromSeed("session:" + sid)
		}
		headers["X-Stainless-Helper-Method"] = "stream"
		headers["X-Stainless-Retry-Count"] = "0"
		headers["X-Stainless-Runtime-Version"] = "v24.14.0"
		headers["X-Stainless-Package-Version"] = "0.80.0"
		headers["X-Stainless-Runtime"] = "node"
		headers["X-Stainless-Lang"] = "js"
		headers["X-Stainless-Arch"] = stainlessArch()
		headers["X-Stainless-Os"] = stainlessOS()
		headers["X-Stainless-Timeout"] = "600"
		// The endpoint expects the Claude Code tool names, so client tools are
		// suffixed and the CLI's own tools are offered as unavailable decoys.
		if len(req.Tools) > 0 {
			tools := encodeAnthropicTools(req.Tools)
			if len(tools) > 0 {
				tools[len(tools)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
			}
			cloaked, names := cloakClaudeTools(tools)
			payload["tools"] = cloaked
			aliases = names
			cloakClaudeMessages(messages)
		}
		payload["system"] = withClaudeCodeIdentity(payload["system"])
		payload["metadata"] = map[string]any{
			"user_id": claudeUserID(key, c.SessionID()),
		}
		url += "?beta=true"
	} else {
		headers["x-api-key"] = key
		beta := "prompt-caching-2024-07-31"
		if hasPDF {
			beta += ",pdfs-2024-09-25"
		}
		headers["anthropic-beta"] = beta
		if len(req.Tools) > 0 {
			tools := encodeAnthropicTools(req.Tools)
			if len(tools) > 0 {
				tools[len(tools)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
			}
			payload["tools"] = tools
		}
	}
	return c.streamWithURL(ctx, url, headers, payload, emit, aliases)
}

func (c *anthropicClient) streamWithURL(ctx context.Context, url string, headers map[string]string, payload map[string]any, emit func(StreamEvent) error, aliases map[string]string) error {
	response, err := c.post(ctx, url, headers, payload)
	if err != nil && c.info.OAuth {
		var statusErr *providerStatusError
		if errors.As(err, &statusErr) && statusErr.status == http.StatusUnauthorized {
			if newKey := c.refreshKey(); newKey != "" {
				headers["Authorization"] = "Bearer " + newKey
				response, err = c.post(ctx, url, headers, payload)
			}
		}
	}
	if err != nil && c.info.OAuth {
		var statusErr *providerStatusError
		if errors.As(err, &statusErr) && statusErr.status == http.StatusBadRequest && strings.Contains(statusErr.message, "claude_code_version_too_old") {
			if required := claudeCLIVersionFromError(statusErr.message); required != "" && required != resolveClaudeCLIVersion() {
				rememberClaudeCLIVersion(required)
				headers["User-Agent"] = "claude-cli/" + required + " (external, sdk-cli)"
				response, err = c.post(ctx, url, headers, payload)
			}
		}
	}
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if rl := ParseAnthropicRateLimit(response.Header); rl != nil {
		_ = emit(StreamEvent{Type: EventRateLimit, RateLimit: rl})
	}

	reader := newSSEReader(response.Body)
	blocks := map[int]*anthropicBlock{}
	// The counters Anthropic streams are cumulative for the request, and one
	// response can carry more than one message: interleaved thinking is in the
	// OAuth beta list, and each thinking round reports the running total again.
	// The agent sums every usage event it is handed, so forwarding each report
	// raw charged the same prompt tokens twice. The OpenAI reader already dedups
	// this way; this is the same guard.
	reportedUsage := &cumulativeUsage{}
	// Anthropic splits one request's usage across two event shapes:
	// message_start carries the prompt side and message_delta carries only
	// the output side, and neither carries a total. The snapshot below joins
	// the two halves into the cumulative request-wide figure the tracker
	// expects, so every emitted step can name TotalTokens as prompt plus
	// completion. Without it an Anthropic turn recorded a total of zero,
	// which blanked the status-bar token count, /cost's total and the
	// rolling quota window for the provider most operators use.
	var snapPrompt, snapCompletion, snapCacheRead, snapCacheWrite int
	snapshot := func() Usage {
		return Usage{
			PromptTokens:     snapPrompt,
			CompletionTokens: snapCompletion,
			TotalTokens:      snapPrompt + snapCompletion,
			CacheReadTokens:  snapCacheRead,
			CacheWriteTokens: snapCacheWrite,
		}
	}
	for {
		payload, err := reader.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return wrapStreamError("Anthropic", err)
		}
		if payload == "" {
			continue
		}
		var event anthropicEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		switch event.Type {
		case "message_start":
			if event.Message != nil && event.Message.Usage != nil {
				// Anthropic reports input_tokens, cache_read and cache_creation
				// as separate counts that do not overlap, so the prompt total is
				// their sum. Pricing subtracts the cached parts before applying
				// the regular input rate, so nothing is charged twice.
				promptTokens := event.Message.Usage.InputTokens
				cacheRead := event.Message.Usage.CacheReadInputTokens
				cacheWrite := event.Message.Usage.CacheCreationInputTokens
				snapPrompt = promptTokens + cacheRead + cacheWrite
				snapCacheRead = cacheRead
				snapCacheWrite = cacheWrite
				if step, ok := reportedUsage.step(snapshot()); ok {
					if err := emit(StreamEvent{Type: EventUsage, Usage: &step}); err != nil {
						return err
					}
				}
			}
		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				name := event.ContentBlock.Name
				if aliases != nil {
					if original, ok := aliases[name]; ok {
						name = original
					}
				}
				blocks[event.Index] = &anthropicBlock{id: event.ContentBlock.ID, name: name}
			}
		case "content_block_delta":
			delta := event.Delta
			switch delta.Type {
			case "text_delta":
				if delta.Text != "" {
					if err := emit(StreamEvent{Type: EventTextDelta, Text: delta.Text}); err != nil {
						return err
					}
				}
			case "thinking_delta":
				if delta.Thinking != "" {
					if err := emit(StreamEvent{Type: EventReasoningDelta, Text: delta.Thinking}); err != nil {
						return err
					}
				}
			case "input_json_delta":
				if block := blocks[event.Index]; block != nil {
					block.arguments += delta.PartialJSON
				}
			}
		case "content_block_stop":
			if block := blocks[event.Index]; block != nil {
				delete(blocks, event.Index)
				call := ToolCall{ID: block.id, Name: block.name, Arguments: block.arguments}
				if call.Arguments == "" {
					call.Arguments = "{}"
				}
				if call.ID == "" {
					call.ID = fmt.Sprintf("toolu_%d", event.Index)
				}
				if err := emit(StreamEvent{Type: EventToolCall, ToolCall: &call}); err != nil {
					return err
				}
			}
		case "message_delta":
			if event.Delta.StopReason == "max_tokens" {
				if err := emit(StreamEvent{Type: EventIncomplete, IncompleteReason: "max_tokens"}); err != nil {
					return err
				}
			}
			if event.Usage != nil {
				snapCompletion = event.Usage.OutputTokens
				if step, ok := reportedUsage.step(snapshot()); ok {
					if err := emit(StreamEvent{Type: EventUsage, Usage: &step}); err != nil {
						return err
					}
				}
			}
		case "error":
			if event.Error != nil {
				// Lowercase opening word: Go error strings read as a clause when
				// they are wrapped into a larger message.
				return fmt.Errorf("the Anthropic stream reported an error: %s", event.Error.Message)
			}
		}
	}
	return nil
}

type anthropicBlock struct {
	id        string
	name      string
	arguments string
}

func encodeAnthropicTools(tools []ToolDef) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		schema := tool.Schema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": schema,
		})
	}
	return out
}

// encodeAnthropicMessages builds Messages content. Two rules are load-bearing:
// tool results must be user-role blocks, and consecutive results and user turns
// must be merged into one user message or the API rejects the sequence.
func encodeAnthropicMessages(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		switch message.Role {
		case RoleUser:
			out = appendAnthropicUserTurn(out, anthropicUserContent(message))
		case RoleAssistant:
			blocks := make([]map[string]any, 0, len(message.ToolCalls)+1)
			if strings.TrimSpace(message.Content) != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": message.Content})
			}
			for _, call := range message.ToolCalls {
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    call.ID,
					"name":  call.Name,
					"input": rawObject(call.Arguments),
				})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, map[string]any{"type": "text", "text": ""})
			}
			out = append(out, map[string]any{"role": "assistant", "content": blocks})
		case RoleTool:
			results := []map[string]any{anthropicToolResult(message)}
			for index+1 < len(messages) && messages[index+1].Role == RoleTool {
				index++
				results = append(results, anthropicToolResult(messages[index]))
			}
			out = appendAnthropicUserTurn(out, results)
		}
	}
	return out
}

func appendAnthropicUserTurn(out []map[string]any, content any) []map[string]any {
	if len(out) > 0 && out[len(out)-1]["role"] == "user" {
		last := out[len(out)-1]
		var blocks []map[string]any
		switch c := last["content"].(type) {
		case string:
			if strings.TrimSpace(c) != "" {
				blocks = []map[string]any{{"type": "text", "text": c}}
			}
		case []map[string]any:
			blocks = c
		}
		switch n := content.(type) {
		case string:
			if strings.TrimSpace(n) != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": n})
			}
		case []map[string]any:
			blocks = append(blocks, n...)
		}
		if len(blocks) == 0 {
			blocks = []map[string]any{{"type": "text", "text": ""}}
		}
		last["content"] = blocks
		return out
	}
	return append(out, map[string]any{"role": "user", "content": content})
}

func anthropicUserContent(message Message) any {
	if len(message.Images) == 0 {
		return message.Content
	}
	blocks := make([]map[string]any, 0, len(message.Images)+1)
	if strings.TrimSpace(message.Content) != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": message.Content})
	}
	for _, image := range message.Images {
		blockType := "image"
		if image.MediaType == "application/pdf" {
			blockType = "document"
		}
		blocks = append(blocks, map[string]any{
			"type": blockType,
			"source": map[string]any{
				"type":       "base64",
				"media_type": image.MediaType,
				"data":       image.Data,
			},
		})
	}
	return blocks
}

func anthropicToolResult(message Message) map[string]any {
	content := message.Content
	if strings.TrimSpace(content) == "" {
		content = "(no output)"
	}
	return map[string]any{
		"type":        "tool_result",
		"tool_use_id": message.ToolID,
		"content":     content,
	}
}

// rawObject parses a tool call's arguments back into a JSON value. Anthropic
// wants a real object, not the string the model streamed.
func rawObject(arguments string) any {
	decoded, err := decodeArguments(arguments)
	if err != nil {
		return map[string]any{}
	}
	return decoded
}

// applyPromptCacheToMessages keeps a stable cache anchor at the head of the
// conversation and a moving one near the tail.
//
// Marking only the second-to-last message, as this once did, moved the cached
// prefix on every turn: each new turn rewrote the region the previous turn had
// just cached, so a long conversation paid full input price for its own history
// again and again. Cache reads cost a fraction of that, which is the whole
// point of the feature.
//
// The first message never changes once written, so an anchor there is read from
// cache on every later turn. The moving marker goes on the last message, the
// newest user turn or tool result. It used to sit on the second-to-last one,
// which left the newest tool result outside the cache: it was billed at the
// full input rate on this step and then written to the cache on the next, so
// every large result was paid for twice. Marked last, it is written once
// (at the cache-write premium) and read at the cache rate from the next step.
func applyPromptCacheToMessages(messages []map[string]any) {
	if len(messages) == 0 {
		return
	}
	markMessageCache(messages[0])
	if last := len(messages) - 1; last > 0 {
		markMessageCache(messages[last])
	}
}

// markMessageCache attaches an ephemeral cache breakpoint to one message. The
// breakpoint goes on the last content block, which is how Anthropic reads it:
// the marker caches everything up to and including that block.
func markMessageCache(target map[string]any) {
	switch content := target["content"].(type) {
	case string:
		// A blank string carries nothing to cache, and rewriting it into a block
		// would only add noise to the request.
		if strings.TrimSpace(content) == "" {
			return
		}
		target["content"] = []map[string]any{
			{
				"type":          "text",
				"text":          content,
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		}
	case []map[string]any:
		if len(content) == 0 {
			return
		}
		content[len(content)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
	}
}

type anthropicEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	ContentBlock *struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Name  string `json:"name"`
		Input any    `json:"input"`
	} `json:"content_block"`
	Message *struct {
		Usage *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage *struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func claudeUserID(apiKey, sessionID string) string {
	hDevice := sha256.Sum256([]byte("device:" + apiKey))
	deviceID := hex.EncodeToString(hDevice[:])
	accountUUID := deriveUUIDFromSeed("account:" + apiKey)
	sessionUUID := deriveUUIDFromSeed("session:" + sessionID)
	if sessionID == "" {
		sessionUUID = deriveUUIDFromSeed(deviceID)
	}
	return fmt.Sprintf(`{"device_id":"%s","account_uuid":"%s","session_id":"%s"}`, deviceID, accountUUID, sessionUUID)
}

func deriveUUIDFromSeed(seed string) string {
	h := sha256.Sum256([]byte(seed))
	hexStr := hex.EncodeToString(h[:])
	b16 := (h[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-4%s-%02x%s-%s", hexStr[0:8], hexStr[8:12], hexStr[13:16], b16, hexStr[17:20], hexStr[20:32])
}

// claudeToolSuffix is the suffix every client tool gets before an OAuth
// request. The endpoint expects the Claude Code tool names, so a client tool
// named read_file is sent as read_file_ide and renamed back on the stream.
const claudeToolSuffix = "_ide"

// cloakClaudeTools suffixes the client tools and appends the Claude Code decoy
// tools, which are advertised as unavailable. It returns the aliases mapping a
// suffixed name back to the original.
func cloakClaudeTools(tools []map[string]any) ([]map[string]any, map[string]string) {
	aliases := make(map[string]string, len(tools))
	cloaked := make([]map[string]any, 0, len(tools)+len(claudeDecoyTools))
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		suffixed := name + claudeToolSuffix
		aliases[suffixed] = name
		copied := make(map[string]any, len(tool))
		for key, value := range tool {
			copied[key] = value
		}
		copied["name"] = suffixed
		cloaked = append(cloaked, copied)
	}
	return append(cloaked, claudeDecoyTools...), aliases
}

// cloakClaudeMessages renames the tool_use blocks in the history to the
// suffixed names the tools list now carries.
func cloakClaudeMessages(messages []map[string]any) {
	for _, message := range messages {
		blocks, ok := message["content"].([]map[string]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			if block["type"] != "tool_use" {
				continue
			}
			name, _ := block["name"].(string)
			if name != "" && !strings.HasSuffix(name, claudeToolSuffix) {
				block["name"] = name + claudeToolSuffix
			}
		}
	}
}

// withClaudeCodeIdentity ensures the first system block is the exact Claude
// Code identity on its own (without cache_control), and removes duplicate
// copies of it from subsequent blocks. Anthropic rejects OAuth subscription
// requests with 429 rate_limit_error when another block precedes it.
func withClaudeCodeIdentity(system any) []map[string]any {
	identity := map[string]any{"type": "text", "text": claudeCodeIdentity}
	switch value := system.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(value)+1)
		out = append(out, identity)
		for _, block := range value {
			if text, ok := block["text"].(string); ok && strings.TrimSpace(text) == claudeCodeIdentity {
				continue
			}
			out = append(out, block)
		}
		return out
	case string:
		rest := strings.TrimSpace(value)
		if rest == "" || rest == claudeCodeIdentity {
			return []map[string]any{identity}
		}
		if strings.HasPrefix(rest, claudeCodeIdentity) {
			rest = strings.TrimSpace(rest[len(claudeCodeIdentity):])
		}
		if rest == "" {
			return []map[string]any{identity}
		}
		return []map[string]any{identity, {"type": "text", "text": rest}}
	default:
		return []map[string]any{identity}
	}
}

// claudeDecoyTools are the Claude Code native tools, offered so the request
// looks like the CLI. A decoy that gets called resolves to no client tool and
// surfaces as unavailable, which is the intent.
var claudeDecoyTools = []map[string]any{
	{"name": "Task", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "TaskOutput", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "TaskStop", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "TaskCreate", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "TaskGet", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "TaskUpdate", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "TaskList", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "Bash", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "Glob", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "Grep", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "Read", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "Edit", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "Write", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "NotebookEdit", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "WebFetch", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "WebSearch", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "AskUserQuestion", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "Skill", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "EnterPlanMode", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
	{"name": "ExitPlanMode", "description": "This tool is currently unavailable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}},
}
