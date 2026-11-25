package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\nfakeimagedata")

// TestShortenKeepsValidUTF8 pins the cut to a character boundary. A byte
// offset that lands inside a multi-byte rune produces invalid UTF-8, which a
// provider rejects and a terminal paints as a replacement glyph.
func TestShortenKeepsValidUTF8(t *testing.T) {
	text := "Kesalahan: 日本語のドキュメントが読めません"
	for limit := 1; limit < 40; limit++ {
		got := shorten(text, limit)
		if !utf8.ValidString(got) {
			t.Fatalf("shorten(%d) = %q, which is not valid UTF-8", limit, got)
		}
	}
}

type recorded struct {
	path  string
	body  map[string]any
	auth  string
	goog  string
	xi    string
	query string
}

// fakeAPI answers every provider endpoint the package calls and records the
// last request per path.
func fakeAPI(t *testing.T, fail map[string]bool) (*httptest.Server, map[string]*recorded) {
	t.Helper()
	seen := map[string]*recorded{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		seen[r.URL.Path] = &recorded{path: r.URL.Path, body: body, auth: r.Header.Get("Authorization"), goog: r.Header.Get("x-goog-api-key"), xi: r.Header.Get("xi-api-key"), query: r.URL.RawQuery}
		if fail[r.URL.Path] {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			return
		}
		encoded := base64.StdEncoding.EncodeToString(pngBytes)
		switch {
		case strings.HasSuffix(r.URL.Path, "/images/generations"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"b64_json": encoded}}})
		case strings.HasSuffix(r.URL.Path, ":predict"):
			_ = json.NewEncoder(w).Encode(map[string]any{"predictions": []any{map[string]any{"bytesBase64Encoded": encoded, "mimeType": "image/png"}}})
		case strings.Contains(r.URL.Path, "tts"):
			pcm := base64.StdEncoding.EncodeToString([]byte{1, 0, 2, 0})
			_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
				map[string]any{"inlineData": map[string]any{"mimeType": "audio/L16;codec=pcm;rate=16000", "data": pcm}},
			}}}}})
		case strings.HasSuffix(r.URL.Path, ":generateContent"):
			_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
				map[string]any{"text": "here it is"},
				map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": encoded}},
			}}}}})
		case strings.Contains(r.URL.Path, "/text-to-speech/"):
			_, _ = w.Write([]byte("ELABSfakeaudio"))
		case strings.HasSuffix(r.URL.Path, "/audio/speech"):
			_, _ = w.Write([]byte("ID3fakeaudio"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, seen
}

func credentialsFor(base string, keyed ...string) Credentials {
	set := map[string]bool{}
	for _, id := range keyed {
		set[id] = true
	}
	return func(id string) (string, string) {
		if !set[id] {
			return "", ""
		}
		if id == "google" {
			return "g-key", base
		}
		return id + "-key", base + "/v1"
	}
}

func TestGenerateImageOpenAI(t *testing.T) {
	server, seen := fakeAPI(t, nil)
	images, used, err := GenerateImage(context.Background(), ImageRequest{Prompt: "a cat", Size: "1024x1024"}, credentialsFor(server.URL, "openai", "google"))
	if err != nil || used != "openai:gpt-image-1" || len(images) != 1 || images[0].Ext() != ".png" {
		t.Fatalf("images=%d used=%q err=%v", len(images), used, err)
	}
	request := seen["/v1/images/generations"]
	if request.auth != "Bearer openai-key" || request.body["size"] != "1024x1024" {
		t.Fatalf("request = %+v", request)
	}
	if _, has := request.body["response_format"]; has {
		t.Fatalf("gpt-image models must not be sent response_format")
	}
}

func TestGenerateImageFallsBackToGemini(t *testing.T) {
	server, seen := fakeAPI(t, map[string]bool{"/v1/images/generations": true})
	images, used, err := GenerateImage(context.Background(), ImageRequest{Prompt: "a cat"}, credentialsFor(server.URL, "openai", "google"))
	if err != nil || used != "google:gemini-2.5-flash-image" || len(images) != 1 {
		t.Fatalf("images=%d used=%q err=%v", len(images), used, err)
	}
	if seen["/v1beta/models/gemini-2.5-flash-image:generateContent"].goog != "g-key" {
		t.Fatalf("Gemini should be called with its key header")
	}
}

func TestGenerateImageInfersProviderAndImagen(t *testing.T) {
	server, seen := fakeAPI(t, nil)
	if _, used, err := GenerateImage(context.Background(), ImageRequest{Prompt: "x", Model: "dall-e-3"}, credentialsFor(server.URL, "openai", "google")); err != nil || used != "openai:dall-e-3" {
		t.Fatalf("used=%q err=%v", used, err)
	}
	if seen["/v1/images/generations"].body["response_format"] != "b64_json" {
		t.Fatalf("dall-e must ask for base64")
	}
	_, used, err := GenerateImage(context.Background(), ImageRequest{Prompt: "x", Model: "imagen-4.0-generate-001", Size: "1920x1080"}, credentialsFor(server.URL, "openai", "google"))
	if err != nil || used != "google:imagen-4.0-generate-001" {
		t.Fatalf("used=%q err=%v", used, err)
	}
	params := seen["/v1beta/models/imagen-4.0-generate-001:predict"].body["parameters"].(map[string]any)
	if params["aspectRatio"] != "16:9" {
		t.Fatalf("aspect ratio = %v", params["aspectRatio"])
	}
}

func TestGenerateImageWithoutKeys(t *testing.T) {
	_, _, err := GenerateImage(context.Background(), ImageRequest{Prompt: "x"}, credentialsFor("http://unused"))
	if !errors.Is(err, ErrNoImageProvider) {
		t.Fatalf("err = %v", err)
	}
}

func TestIsImageGenerationModel(t *testing.T) {
	for spec, want := range map[string]bool{
		"dall-e-3": true, "openai:gpt-image-1": true, "google:gemini-2.5-flash-image": true,
		"imagen-4.0-generate-001": true, "openai:gpt-4o": false, "gemini-2.5-flash": false, "": false,
	} {
		if got := IsImageGenerationModel(spec); got != want {
			t.Errorf("%q = %v, want %v", spec, got, want)
		}
	}
}

func TestSpeakOpenAIAndGemini(t *testing.T) {
	server, seen := fakeAPI(t, nil)
	clip, used, err := Speak(context.Background(), SpeechRequest{Text: "halo", Format: "opus"}, credentialsFor(server.URL, "openai"))
	if err != nil || used != "openai:gpt-4o-mini-tts" || clip.Ext != ".ogg" || string(clip.Data) != "ID3fakeaudio" {
		t.Fatalf("clip=%+v used=%q err=%v", clip, used, err)
	}
	if seen["/v1/audio/speech"].body["voice"] != "alloy" {
		t.Fatalf("default voice not sent")
	}

	clip, used, err = Speak(context.Background(), SpeechRequest{Text: "halo", Model: "google:gemini-2.5-flash-preview-tts"}, credentialsFor(server.URL, "google"))
	if err != nil || used != "google:gemini-2.5-flash-preview-tts" || clip.Ext != ".wav" {
		t.Fatalf("clip=%+v used=%q err=%v", clip, used, err)
	}
	if string(clip.Data[:4]) != "RIFF" || len(clip.Data) != 44+4 {
		t.Fatalf("the PCM should be wrapped in a 44-byte WAV header, got %d bytes", len(clip.Data))
	}
	if rate := int(clip.Data[24]) | int(clip.Data[25])<<8; rate != 16000 {
		t.Fatalf("sample rate = %d, want the one the MIME type names", rate)
	}
}

func TestSpeakFallsBackToTheSystemVoice(t *testing.T) {
	original := systemSpeaker
	t.Cleanup(func() { systemSpeaker = original })
	var spoken string
	systemSpeaker = func(_ context.Context, _, textPath, _ string) ([]byte, string, string, error) {
		data, _ := readFile(textPath)
		spoken = string(data)
		return []byte("RIFFwav"), "audio/wav", ".wav", nil
	}
	clip, used, err := Speak(context.Background(), SpeechRequest{Text: "tanpa kunci; rm -rf /"}, credentialsFor("http://unused"))
	if err != nil || used != "system" || clip.Ext != ".wav" {
		t.Fatalf("clip=%+v used=%q err=%v", clip, used, err)
	}
	if spoken != "tanpa kunci; rm -rf /" {
		t.Fatalf("the text should reach the speaker through a file, got %q", spoken)
	}
}

func TestSpeakExplicitProviderDoesNotFallBack(t *testing.T) {
	server, _ := fakeAPI(t, map[string]bool{"/v1/audio/speech": true})
	original := systemSpeaker
	t.Cleanup(func() { systemSpeaker = original })
	systemSpeaker = func(context.Context, string, string, string) ([]byte, string, string, error) {
		t.Fatalf("an explicit provider must not silently fall back")
		return nil, "", "", nil
	}
	if _, _, err := Speak(context.Background(), SpeechRequest{Text: "x", Model: "openai:tts-1"}, credentialsFor(server.URL, "openai")); err == nil {
		t.Fatalf("a failing explicit provider should error")
	}
}

func TestSpeakElevenLabs(t *testing.T) {
	server, seen := fakeAPI(t, nil)
	clip, used, err := Speak(context.Background(), SpeechRequest{Text: "halo dunia", Model: "elevenlabs", Format: "opus"}, credentialsFor(server.URL, "elevenlabs"))
	if err != nil || used != "elevenlabs:eleven_multilingual_v2" || clip.Ext != ".ogg" || string(clip.Data) != "ELABSfakeaudio" {
		t.Fatalf("clip=%+v used=%q err=%v", clip, used, err)
	}
	request := seen["/v1/text-to-speech/EXAVITQu4vr4xnSDxMaL"]
	if request == nil || request.xi != "elevenlabs-key" {
		t.Fatalf("request = %+v", request)
	}
	if request.body["model_id"] != "eleven_multilingual_v2" || request.body["text"] != "halo dunia" {
		t.Fatalf("body = %+v", request.body)
	}
	if request.query != "output_format=opus_48000_128" {
		t.Fatalf("query = %q", request.query)
	}

	clip, used, err = Speak(context.Background(), SpeechRequest{Text: "halo", Model: "elevenlabs:eleven_flash_v2_5", Voice: "voice-xyz"}, credentialsFor(server.URL, "elevenlabs"))
	if err != nil || used != "elevenlabs:eleven_flash_v2_5" || clip.Ext != ".mp3" {
		t.Fatalf("clip=%+v used=%q err=%v", clip, used, err)
	}
	if seen["/v1/text-to-speech/voice-xyz"] == nil {
		t.Fatalf("the named voice should be in the path, seen %v", seen)
	}
}

func TestSpeakElevenLabsStaysOutOfTheDefaultOrder(t *testing.T) {
	server, seen := fakeAPI(t, nil)
	original := systemSpeaker
	t.Cleanup(func() { systemSpeaker = original })
	systemSpeaker = func(context.Context, string, string, string) ([]byte, string, string, error) {
		return []byte("RIFFwav"), "audio/wav", ".wav", nil
	}
	_, used, err := Speak(context.Background(), SpeechRequest{Text: "halo"}, credentialsFor(server.URL, "elevenlabs"))
	if err != nil || used != "system" {
		t.Fatalf("used=%q err=%v", used, err)
	}
	for path := range seen {
		if strings.Contains(path, "/text-to-speech/") {
			t.Fatalf("the default order must not call ElevenLabs, saw %s", path)
		}
	}
}

func TestSpeakElevenLabsWithoutKey(t *testing.T) {
	original := systemSpeaker
	t.Cleanup(func() { systemSpeaker = original })
	systemSpeaker = func(context.Context, string, string, string) ([]byte, string, string, error) {
		t.Fatalf("an explicit provider must not silently fall back")
		return nil, "", "", nil
	}
	if _, _, err := Speak(context.Background(), SpeechRequest{Text: "x", Model: "elevenlabs:eleven_multilingual_v2"}, credentialsFor("http://unused")); err == nil {
		t.Fatalf("a missing key should error")
	}
}
