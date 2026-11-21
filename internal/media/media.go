// Package media generates images and speech through the providers that offer
// them: OpenAI and xAI image endpoints, Gemini image and speech models, the
// OpenAI speech endpoint, and the operating system's own voice as the last
// resort for speech. Each backend is plain HTTP, so it needs no SDK.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// requestTimeout bounds one generation. A large image takes up to a minute.
const requestTimeout = 3 * time.Minute

// maxResponseBytes bounds a response body: four large PNGs in base64.
const maxResponseBytes = 64 << 20

// Credentials resolves a provider's key and base URL. An empty key means the
// provider is not configured.
type Credentials func(providerID string) (key, baseURL string)

// splitModel reads "provider:model" and returns both halves. A bare model has
// no provider.
func splitModel(spec string) (string, string) {
	spec = strings.TrimSpace(spec)
	if index := strings.Index(spec, ":"); index > 0 {
		return strings.ToLower(spec[:index]), spec[index+1:]
	}
	return "", spec
}

func postJSON(ctx context.Context, url string, headers map[string]string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s", response.Status, apiMessage(data))
	}
	return data, nil
}

// apiMessage pulls the human message out of an error body.
func apiMessage(data []byte) string {
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &payload) == nil && len(payload.Error) > 0 {
		var detail struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(payload.Error, &detail) == nil && detail.Message != "" {
			return detail.Message
		}
		var text string
		if json.Unmarshal(payload.Error, &text) == nil && text != "" {
			return text
		}
	}
	text := strings.TrimSpace(string(data))
	if len(text) > 300 {
		text = text[:300] + "..."
	}
	return text
}

func bearer(key string) map[string]string { return map[string]string{"Authorization": "Bearer " + key} }

func googleHeaders(key string) map[string]string { return map[string]string{"x-goog-api-key": key} }

// googleModelURL is the generateContent endpoint of one Gemini model.
func googleModelURL(base, model string) string {
	base = strings.TrimRight(base, "/")
	if !strings.Contains(base, "/v1") {
		base += "/v1beta"
	}
	return base + "/models/" + model + ":generateContent"
}

// googleInlineParts reads the inline data parts of a generateContent answer.
func googleInlineParts(data []byte) ([]inlinePart, string, error) {
	var answer struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text       string      `json:"text"`
					InlineData *inlinePart `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return nil, "", fmt.Errorf("unreadable Gemini answer: %w", err)
	}
	if answer.PromptFeedback.BlockReason != "" {
		return nil, "", fmt.Errorf("gemini refused the prompt (%s)", answer.PromptFeedback.BlockReason)
	}
	var parts []inlinePart
	var text []string
	for _, candidate := range answer.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.InlineData != nil && part.InlineData.Data != "" {
				parts = append(parts, *part.InlineData)
			}
			if strings.TrimSpace(part.Text) != "" {
				text = append(text, strings.TrimSpace(part.Text))
			}
		}
	}
	return parts, strings.Join(text, "\n"), nil
}

type inlinePart struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}
