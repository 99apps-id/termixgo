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

func TestTranscribeAudioFailsWhenNoKey(t *testing.T) {
	app, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New app: %v", err)
	}
	defer app.Shutdown()

	// Clear groq and openai secrets in store
	_ = app.store.Delete(secrets.ProviderKey("groq"))
	_ = app.store.Delete(secrets.ProviderKey("openai"))

	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	_, err = app.TranscribeAudio(context.Background(), []byte("audio"), "voice.ogg")
	if err == nil {
		t.Fatalf("expected error when no speech-to-text key is configured")
	}
	if !strings.Contains(err.Error(), "no speech-to-text API key found") {
		t.Errorf("expected key guidance in error, got: %v", err)
	}
}
