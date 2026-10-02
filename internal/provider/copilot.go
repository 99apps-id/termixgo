package provider

import (
	"context"
	"fmt"
	"strings"
)

// copilotClient routes completions to GitHub Copilot's chat or messages backend.
type copilotClient struct {
	*httpClient
}

func (c *copilotClient) Stream(ctx context.Context, req ChatRequest, emit func(StreamEvent) error) error {
	key := c.currentKey()
	isClaude := strings.Contains(strings.ToLower(req.Model), "claude")

	reqID := fmt.Sprintf("copilot-%s", deriveSessionID(c.apiKey))
	headers := map[string]string{
		"Authorization":                       "Bearer " + key,
		"Content-Type":                        "application/json",
		"copilot-integration-id":              "vscode-chat",
		"editor-version":                      "vscode/1.110.0",
		"editor-plugin-version":               "copilot-chat/0.38.0",
		"user-agent":                          "GitHubCopilotChat/0.38.0",
		"openai-intent":                       "conversation-panel",
		"x-github-api-version":                "2025-04-01",
		"x-request-id":                        reqID,
		"x-vscode-user-agent-library-version": "electron-fetch",
		"X-Initiator":                         "user",
		"session_id":                          c.SessionID(),
	}

	if isClaude {
		headers["anthropic-version"] = "2023-06-01"
		anthropicCli := &anthropicClient{httpClient: c.httpClient}
		url := c.baseURL + "/v1/messages"
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
		return anthropicCli.streamWithURL(ctx, url, headers, payload, emit)
	}

	openAICli := &openAIClient{httpClient: c.httpClient}
	url := c.baseURL + "/chat/completions"
	payload := map[string]any{
		"model":    req.Model,
		"stream":   true,
		"messages": openAICli.encodeMessages(req),
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if len(req.Tools) > 0 {
		payload["tools"] = encodeOpenAITools(req.Tools)
		payload["tool_choice"] = "auto"
	}
	return openAICli.streamWithURL(ctx, url, headers, payload, emit)
}
