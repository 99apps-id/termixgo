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

const (
	transcriptionTimeout      = 45 * time.Second
	defaultElevenLabsSTTModel = "scribe_v2"
	defaultElevenLabsBaseURL  = "https://api.elevenlabs.io/v1"
)

// TranscribeAudio transcribes voice audio using the configured VoiceModel, or
// Groq Whisper, OpenAI Whisper, ElevenLabs Scribe, or an OpenAI-compatible API.
func (a *App) TranscribeAudio(ctx context.Context, data []byte, filename string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty audio data")
	}

	// Snapshot the config under the lock: the Telegram/WhatsApp bridges
	// transcribe on their own goroutines while the UI can be editing settings.
	a.mu.Lock()
	configuredVoice := strings.TrimSpace(a.cfg.VoiceModel)
	customURL, hasCustom := a.cfg.BaseURLs["openai-compatible"]
	elevenLabsBase := strings.TrimSpace(a.cfg.BaseURLs["elevenlabs"])
	a.mu.Unlock()

	if elevenLabsBase == "" {
		elevenLabsBase = defaultElevenLabsBaseURL
	}
	elevenLabsEndpoint := strings.TrimRight(elevenLabsBase, "/") + "/speech-to-text"

	var lastErr error
	attempted := false

	// 0. If a voice model is configured explicitly, try it first.
	if vm := configuredVoice; vm != "" {
		providerID := ""
		modelID := vm
		if idx := strings.Index(vm, ":"); idx >= 0 {
			providerID = strings.ToLower(vm[:idx])
			modelID = vm[idx+1:]
		}
		if (providerID == "groq" || providerID == "") && provider.ResolveKey(a.store, "groq") != "" {
			attempted = true
			key := provider.ResolveKey(a.store, "groq")
			text, err := transcribeViaOpenAICompatible(ctx, "https://api.groq.com/openai/v1/audio/transcriptions", key, modelID, filename, data)
			if err == nil && text != "" {
				return text, nil
			}
			if err != nil {
				lastErr = err
			}
		}
		if (providerID == "openai" || providerID == "") && provider.ResolveKey(a.store, "openai") != "" {
			attempted = true
			key := provider.ResolveKey(a.store, "openai")
			text, err := transcribeViaOpenAICompatible(ctx, "https://api.openai.com/v1/audio/transcriptions", key, modelID, filename, data)
			if err == nil && text != "" {
				return text, nil
			}
			if err != nil {
				lastErr = err
			}
		}
		if (providerID == "elevenlabs" || providerID == "") && provider.ResolveKey(a.store, "elevenlabs") != "" {
			attempted = true
			key := provider.ResolveKey(a.store, "elevenlabs")
			m := strings.TrimSpace(modelID)
			if m == "" || m == "elevenlabs" {
				m = defaultElevenLabsSTTModel
			}
			text, err := transcribeViaElevenLabs(ctx, elevenLabsEndpoint, key, m, filename, data)
			if err == nil && text != "" {
				return text, nil
			}
			if err != nil {
				lastErr = err
			}
		}
	}

	// 1. Try Groq Whisper (fast transcription with large-v3-turbo)
	if key := provider.ResolveKey(a.store, "groq"); key != "" {
		attempted = true
		text, err := transcribeViaOpenAICompatible(ctx, "https://api.groq.com/openai/v1/audio/transcriptions", key, "whisper-large-v3-turbo", filename, data)
		if err == nil && text != "" {
			return text, nil
		}
		if err != nil {
			lastErr = err
		}
	}

	// 2. Try OpenAI Whisper
	if key := provider.ResolveKey(a.store, "openai"); key != "" {
		attempted = true
		text, err := transcribeViaOpenAICompatible(ctx, "https://api.openai.com/v1/audio/transcriptions", key, "whisper-1", filename, data)
		if err == nil && text != "" {
			return text, nil
		}
		if err != nil {
			lastErr = err
		}
	}

	// 3. Try ElevenLabs Speech-to-Text (Scribe)
	if key := provider.ResolveKey(a.store, "elevenlabs"); key != "" {
		attempted = true
		text, err := transcribeViaElevenLabs(ctx, elevenLabsEndpoint, key, defaultElevenLabsSTTModel, filename, data)
		if err == nil && text != "" {
			return text, nil
		}
		if err != nil {
			lastErr = err
		}
	}

	// 4. Try custom OpenAI-compatible endpoint if configured
	if hasCustom && customURL != "" {
		if key := provider.ResolveKey(a.store, "openai-compatible"); key != "" {
			attempted = true
			endpoint := strings.TrimRight(customURL, "/") + "/audio/transcriptions"
			text, err := transcribeViaOpenAICompatible(ctx, endpoint, key, "whisper-1", filename, data)
			if err == nil && text != "" {
				return text, nil
			}
			if err != nil {
				lastErr = err
			}
		}
	}

	if attempted && lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no speech-to-text API key found (configure Groq, OpenAI, or ElevenLabs via 'termixgo secret groq', 'termixgo secret openai', or 'termixgo secret elevenlabs')")
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

func transcribeViaElevenLabs(ctx context.Context, endpoint, apiKey, modelID, filename string, data []byte) (string, error) {
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
	if err := writer.WriteField("model_id", modelID); err != nil {
		return "", fmt.Errorf("write form model_id: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", fmt.Errorf("create ElevenLabs request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("xi-api-key", strings.TrimSpace(apiKey))

	client := &http.Client{Timeout: transcriptionTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ElevenLabs transcription request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read ElevenLabs transcription response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ElevenLabs transcription failed (%s): %s", resp.Status, parseElevenLabsSTTError(respBody))
	}

	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return "", fmt.Errorf("parse ElevenLabs transcription response: %w", err)
	}
	return strings.TrimSpace(payload.Text), nil
}

func parseElevenLabsSTTError(respBody []byte) string {
	var structured struct {
		Detail struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(respBody, &structured); err == nil && strings.TrimSpace(structured.Detail.Message) != "" {
		if structured.Detail.Status != "" {
			return structured.Detail.Status + ": " + strings.TrimSpace(structured.Detail.Message)
		}
		return strings.TrimSpace(structured.Detail.Message)
	}
	var simple struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(respBody, &simple); err == nil && strings.TrimSpace(simple.Detail) != "" {
		return strings.TrimSpace(simple.Detail)
	}
	msg := strings.TrimSpace(string(respBody))
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	return msg
}
