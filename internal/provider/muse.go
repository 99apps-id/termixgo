package provider

import (
	"context"
	"strings"
)

// museUserAgent is the identity Meta's Muse backend expects. It gates access,
// so the official CLI's build string is reused rather than the Termixgo agent.
const museUserAgent = "muse-build/1.3.0 (interactive; macos-aarch64; build ac7280f2aca67769d1455a8847bb502b617d50f6)"

// museClient speaks the Responses API served at api.meta.ai for a Muse login.
// It is not chat-completions: the request carries input items and the stream is
// a typed event feed, so it shares the Codex encoder and decoder. Only the
// headers differ, plus the absence of the responses-lite rewrite.
type museClient struct{ *httpClient }

func (c *museClient) Stream(ctx context.Context, req ChatRequest, emit func(StreamEvent) error) error {
	input := encodeResponsesInput(req)
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

	headers := map[string]string{
		"Authorization": "Bearer " + c.currentKey(),
		"User-Agent":    museUserAgent,
		"X-Client-Id":   "tbh:tui",
	}

	response, err := c.post(ctx, c.baseURL+"/responses", headers, payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return decodeResponsesStream(c.info.Label, response.Body, emit)
}
