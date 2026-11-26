package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/secrets"
)

func TestTranscribeAudioWithServer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("expected multipart/form-data, got %s", r.Header.Get("Content-Type"))
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			t.Errorf("expected Bearer test-key, got %s", auth)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"text": "transcribed speech from audio",
		})
	}))
	defer ts.Close()

	text, err := transcribeViaOpenAICompatible(context.Background(), ts.URL, "test-key", "whisper-1", "test.ogg", []byte("dummy-audio"))
	if err != nil {
		t.Fatalf("transcribe error: %v", err)
	}
	if text != "transcribed speech from audio" {
		t.Errorf("expected transcribed text, got %q", text)
	}
}

func TestTranscribeAudioPlainTextFallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "plain text transcription")
	}))
	defer ts.Close()

	text, err := transcribeViaOpenAICompatible(context.Background(), ts.URL, "test-key", "whisper-1", "test.ogg", []byte("dummy-audio"))
	if err != nil {
		t.Fatalf("transcribe error: %v", err)
	}
	if text != "plain text transcription" {
		t.Errorf("expected plain text, got %q", text)
	}
}

func TestTranscribeAudioViaElevenLabsFallback(t *testing.T) {
	var gotPath, gotAPIKey, gotModelID, gotFilename string
	var gotBytes []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("xi-api-key")
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			return
		}
		gotModelID = r.FormValue("model_id")
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("form file: %v", err)
			return
		}
		defer f.Close()
		gotFilename = hdr.Filename
		gotBytes, _ = io.ReadAll(f)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"language_code":        "id",
			"language_probability": 0.99,
			"text":                 "halo dari pesan suara",
		})
	}))
	defer ts.Close()

	app := newTestApp(t)
	_ = app.store.Delete(secrets.ProviderKey("groq"))
	_ = app.store.Delete(secrets.ProviderKey("openai"))
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	if err := app.store.Set(secrets.ProviderKey("elevenlabs"), "sk_test_eleven"); err != nil {
		t.Fatalf("set elevenlabs secret: %v", err)
	}
	app.mu.Lock()
	if app.cfg.BaseURLs == nil {
		app.cfg.BaseURLs = map[string]string{}
	}
	app.cfg.BaseURLs["elevenlabs"] = ts.URL + "/v1"
	app.mu.Unlock()

	text, err := app.TranscribeAudio(context.Background(), []byte("OggS-voice-bytes"), "voice.ogg")
	if err != nil {
		t.Fatalf("TranscribeAudio elevenlabs error: %v", err)
	}
	if text != "halo dari pesan suara" {
		t.Errorf("text = %q, want %q", text, "halo dari pesan suara")
	}
	if gotPath != "/v1/speech-to-text" {
		t.Errorf("path = %q, want /v1/speech-to-text", gotPath)
	}
	if gotAPIKey != "sk_test_eleven" {
		t.Errorf("xi-api-key = %q, want sk_test_eleven", gotAPIKey)
	}
	if gotModelID != "scribe_v2" {
		t.Errorf("model_id = %q, want scribe_v2", gotModelID)
	}
	if gotFilename != "voice.ogg" {
		t.Errorf("filename = %q, want voice.ogg", gotFilename)
	}
	if string(gotBytes) != "OggS-voice-bytes" {
		t.Errorf("file bytes = %q, want OggS-voice-bytes", string(gotBytes))
	}
}

func TestTranscribeViaElevenLabsStructuredError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":{"status":"invalid_api_key","message":"Invalid API key"}}`)
	}))
	defer ts.Close()

	_, err := transcribeViaElevenLabs(context.Background(), ts.URL, "bad-key", "scribe_v2", "voice.ogg", []byte("abc"))
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "invalid_api_key: Invalid API key") {
		t.Errorf("expected parsed detail error, got: %v", err)
	}
}

func TestTranscribeAudioFailsWhenNoKey(t *testing.T) {
	app := newTestApp(t)

	// Clear groq, openai, and elevenlabs secrets in store
	_ = app.store.Delete(secrets.ProviderKey("groq"))
	_ = app.store.Delete(secrets.ProviderKey("openai"))
	_ = app.store.Delete(secrets.ProviderKey("elevenlabs"))

	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ELEVENLABS_API_KEY", "")

	_, err := app.TranscribeAudio(context.Background(), []byte("audio"), "voice.ogg")
	if err == nil {
		t.Fatalf("expected error when no speech-to-text key is configured")
	}
	if !strings.Contains(err.Error(), "no speech-to-text API key found") {
		t.Errorf("expected key guidance in error, got: %v", err)
	}
}
