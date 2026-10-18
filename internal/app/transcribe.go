package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/provider"
)

const transcriptionTimeout = 45 * time.Second

// TranscribeAudio transcribes voice audio using the configured VoiceModel, or Groq/OpenAI Whisper API.
func (a *App) TranscribeAudio(ctx context.Context, data []byte, filename string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty audio data")
	}

	// Snapshot the config under the lock: the Telegram bridge transcribes on
	// its own goroutine while the UI can be editing the settings.
	a.mu.Lock()
	configuredVoice := strings.TrimSpace(a.cfg.VoiceModel)
	customURL, hasCustom := a.cfg.BaseURLs["openai-compatible"]
	a.mu.Unlock()

	// 0. If a voice model is configured explicitly, try it first.
	if vm := configuredVoice; vm != "" {
		providerID := ""
		modelID := vm
		if idx := strings.Index(vm, ":"); idx >= 0 {
			providerID = strings.ToLower(vm[:idx])
			modelID = vm[idx+1:]
		}
		if (providerID == "groq" || providerID == "") && provider.ResolveKey(a.store, "groq") != "" {
			key := provider.ResolveKey(a.store, "groq")
			text, err := transcribeViaOpenAICompatible(ctx, "https://api.groq.com/openai/v1/audio/transcriptions", key, modelID, filename, data)
			if err == nil && text != "" {
				return text, nil
			}
		}
		if (providerID == "openai" || providerID == "") && provider.ResolveKey(a.store, "openai") != "" {
			key := provider.ResolveKey(a.store, "openai")
			text, err := transcribeViaOpenAICompatible(ctx, "https://api.openai.com/v1/audio/transcriptions", key, modelID, filename, data)
			if err == nil && text != "" {
				return text, nil
			}
		}
	}

	// 1. Try Groq Whisper (fast transcription with large-v3-turbo)
	if key := provider.ResolveKey(a.store, "groq"); key != "" {
		text, err := transcribeViaOpenAICompatible(ctx, "https://api.groq.com/openai/v1/audio/transcriptions", key, "whisper-large-v3-turbo", filename, data)
		if err == nil && text != "" {
			return text, nil
		}
	}

	// 2. Try OpenAI Whisper
	if key := provider.ResolveKey(a.store, "openai"); key != "" {
		text, err := transcribeViaOpenAICompatible(ctx, "https://api.openai.com/v1/audio/transcriptions", key, "whisper-1", filename, data)
		if err == nil && text != "" {
			return text, nil
		}
	}

	// 3. Try custom OpenAI-compatible endpoint if configured
	if hasCustom && customURL != "" {
		if key := provider.ResolveKey(a.store, "openai-compatible"); key != "" {
			endpoint := strings.TrimRight(customURL, "/") + "/audio/transcriptions"
			text, err := transcribeViaOpenAICompatible(ctx, endpoint, key, "whisper-1", filename, data)
			if err == nil && text != "" {
				return text, nil
			}
		}
	}

	return "", fmt.Errorf("no speech-to-text API key found (configure Groq or OpenAI via 'termixgo secret groq' or 'termixgo secret openai')")
}

func transcribeViaOpenAICompatible(ctx context.Context, endpoint, apiKey, model, filename string, data []byte) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, transcriptionTimeout)
	defer cancel()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if filename == "" {
		filename = "audio.ogg"
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("write form file: %w", err)
	}
	if err := writer.WriteField("model", model); err != nil {
		return "", fmt.Errorf("write form model: %w", err)
	}
	if err := writer.WriteField("response_format", "json"); err != nil {
		return "", fmt.Errorf("write form response_format: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: transcriptionTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcription request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read transcription response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("transcription failed (%s): %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		text := strings.TrimSpace(string(respBody))
		if text != "" {
			return text, nil
		}
		return "", fmt.Errorf("parse transcription response: %w", err)
	}
	return strings.TrimSpace(payload.Text), nil
}
