package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// anthropicClient speaks the Messages streaming protocol.
type anthropicClient struct{ *httpClient }

func (c *anthropicClient) Stream(ctx context.Context, req ChatRequest, emit func(StreamEvent) error) error {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	payload := map[string]any{
		"model":      req.Model,
		"max_tokens": maxTokens,
		"stream":     true,
		"messages":   encodeAnthropicMessages(req.Messages),
	}
	if strings.TrimSpace(req.System) != "" {
		payload["system"] = req.System
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if len(req.Tools) > 0 {
		payload["tools"] = encodeAnthropicTools(req.Tools)
	}

	headers := map[string]string{"anthropic-version": "2023-06-01"}
	if c.info.OAuth {
		// An OAuth login sends a bearer token and needs the beta that unlocks
		// it, instead of the API key header.
		headers["Authorization"] = "Bearer " + c.apiKey
		headers["anthropic-beta"] = "oauth-2025-04-20"
		headers["x-app"] = "cli"
	} else {
		headers["x-api-key"] = c.apiKey
	}
	response, err := c.post(ctx, c.baseURL+"/v1/messages", headers, payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	reader := newSSEReader(response.Body)
	blocks := map[int]*anthropicBlock{}
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
				usage := Usage{PromptTokens: event.Message.Usage.InputTokens}
				if err := emit(StreamEvent{Type: EventUsage, Usage: &usage}); err != nil {
					return err
				}
			}
		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				blocks[event.Index] = &anthropicBlock{id: event.ContentBlock.ID, name: event.ContentBlock.Name}
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
			if event.Usage != nil {
				usage := Usage{CompletionTokens: event.Usage.OutputTokens}
				if err := emit(StreamEvent{Type: EventUsage, Usage: &usage}); err != nil {
					return err
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
// tool results must be user-role blocks, and consecutive results must be
// merged into one user message or the API rejects the sequence.
func encodeAnthropicMessages(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		switch message.Role {
		case RoleUser:
			out = append(out, map[string]any{"role": "user", "content": anthropicUserContent(message)})
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
			out = append(out, map[string]any{"role": "user", "content": results})
		}
	}
	return out
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
		blocks = append(blocks, map[string]any{
			"type": "image",
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

type anthropicEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
	ContentBlock *struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Name  string `json:"name"`
		Input any    `json:"input"`
	} `json:"content_block"`
	Message *struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}
