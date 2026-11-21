package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// maxSpeechChars bounds one request. OpenAI caps input at 4096 characters.
const maxSpeechChars = 4096

// SpeechRequest asks for spoken audio.
type SpeechRequest struct {
	Text string
	// Model is "provider:model", "system" for the operating system voice, or
	// empty for the default order.
	Model string
	Voice string
	// Format is mp3, opus, wav or aac. Gemini and the system voice always
	// answer wav.
	Format string
	// Instructions steer tone and pace on models that accept them.
	Instructions string
}

// Speech is one rendered clip.
type Speech struct {
	Data []byte
	MIME string
	Ext  string
}

var speechBackends = []struct{ provider, model, voice string }{
	{"openai", "gpt-4o-mini-tts", "alloy"},
	{"google", "gemini-2.5-flash-preview-tts", "Kore"},
}

// Speak renders text as audio, trying the requested model, then each
// configured provider, then the operating system voice. It returns the clip
// and the backend used.
func Speak(ctx context.Context, request SpeechRequest, credentials Credentials) (Speech, string, error) {
	text := strings.TrimSpace(request.Text)
	if text == "" {
		return Speech{}, "", errors.New("text is required")
	}
	if len([]rune(text)) > maxSpeechChars {
		return Speech{}, "", fmt.Errorf("text is %d characters; split it into parts of at most %d", len([]rune(text)), maxSpeechChars)
	}
	request.Text = text
	wantProvider, wantModel := splitModel(request.Model)
	if strings.EqualFold(strings.TrimSpace(request.Model), "system") {
		wantProvider = "system"
	}
	var failures []string
	if wantProvider != "system" {
		for _, backend := range speechBackends {
			if wantProvider != "" && wantProvider != backend.provider {
				continue
			}
			key, base := credentials(backend.provider)
			if key == "" {
				continue
			}
			model := backend.model
			if wantModel != "" {
				model = wantModel
			}
			voice := backend.voice
			if strings.TrimSpace(request.Voice) != "" {
				voice = strings.TrimSpace(request.Voice)
			}
			var clip Speech
			var err error
			if backend.provider == "google" {
				clip, err = speakGemini(ctx, model, voice, key, base, request)
			} else {
				clip, err = speakOpenAI(ctx, model, voice, key, base, request)
			}
			if err == nil {
				return clip, backend.provider + ":" + model, nil
			}
			failures = append(failures, fmt.Sprintf("%s:%s: %v", backend.provider, model, err))
			if wantProvider != "" {
				return Speech{}, "", fmt.Errorf("speech failed: %s", strings.Join(failures, "; "))
			}
		}
		if wantProvider != "" && len(failures) == 0 {
			return Speech{}, "", fmt.Errorf("%s has no key or does not produce speech", wantProvider)
		}
	}
	clip, err := speakSystem(ctx, request)
	if err == nil {
		return clip, "system", nil
	}
	failures = append(failures, "system: "+err.Error())
	return Speech{}, "", fmt.Errorf("speech failed: %s", strings.Join(failures, "; "))
}

func speakOpenAI(ctx context.Context, model, voice, key, base string, request SpeechRequest) (Speech, error) {
	format := strings.ToLower(strings.TrimSpace(request.Format))
	switch format {
	case "mp3", "opus", "aac", "wav", "flac":
	default:
		format = "mp3"
	}
	payload := map[string]any{"model": model, "input": request.Text, "voice": voice, "response_format": format}
	if strings.TrimSpace(request.Instructions) != "" && strings.Contains(model, "gpt-4o") {
		payload["instructions"] = request.Instructions
	}
	data, err := postJSON(ctx, strings.TrimRight(base, "/")+"/audio/speech", bearer(key), payload)
	if err != nil {
		return Speech{}, err
	}
	if len(data) == 0 {
		return Speech{}, errors.New("empty audio")
	}
	mime, ext := audioType(format)
	return Speech{Data: data, MIME: mime, Ext: ext}, nil
}

func audioType(format string) (string, string) {
	switch format {
	case "opus":
		return "audio/ogg", ".ogg"
	case "aac":
		return "audio/aac", ".aac"
	case "wav":
		return "audio/wav", ".wav"
	case "flac":
		return "audio/flac", ".flac"
	default:
		return "audio/mpeg", ".mp3"
	}
}

// speakGemini calls a Gemini speech model, which answers raw 16-bit PCM; the
// samples are wrapped in a WAV header so any player opens the file.
func speakGemini(ctx context.Context, model, voice, key, base string, request SpeechRequest) (Speech, error) {
	text := request.Text
	if strings.TrimSpace(request.Instructions) != "" {
		text = strings.TrimSpace(request.Instructions) + ": " + text
	}
	payload := map[string]any{
		"contents": []any{map[string]any{"parts": []any{map[string]any{"text": text}}}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{
				"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": voice}},
			},
		},
	}
	data, err := postJSON(ctx, googleModelURL(base, model), googleHeaders(key), payload)
	if err != nil {
		return Speech{}, err
	}
	parts, answerText, err := googleInlineParts(data)
	if err != nil {
		return Speech{}, err
	}
	for _, part := range parts {
		if !strings.HasPrefix(part.MimeType, "audio/") {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(part.Data)
		if err != nil || len(raw) == 0 {
			continue
		}
		if strings.Contains(part.MimeType, "wav") {
			return Speech{Data: raw, MIME: "audio/wav", Ext: ".wav"}, nil
		}
		return Speech{Data: pcmToWAV(raw, sampleRate(part.MimeType, 24000)), MIME: "audio/wav", Ext: ".wav"}, nil
	}
	if answerText != "" {
		return Speech{}, fmt.Errorf("the model answered with text instead of audio: %s", shorten(answerText, 200))
	}
	return Speech{}, errors.New("no audio in the answer")
}

// sampleRate reads rate=N from a MIME type such as audio/L16;codec=pcm;rate=24000.
func sampleRate(mime string, fallback int) int {
	for _, field := range strings.Split(mime, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		if ok && strings.EqualFold(name, "rate") {
			if rate, err := strconv.Atoi(value); err == nil && rate > 0 {
				return rate
			}
		}
	}
	return fallback
}

// pcmToWAV wraps mono 16-bit little-endian PCM in a WAV header.
func pcmToWAV(pcm []byte, rate int) []byte {
	var out bytes.Buffer
	const channels, bits = 1, 16
	byteRate := rate * channels * bits / 8
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(36+len(pcm)))
	out.WriteString("WAVEfmt ")
	_ = binary.Write(&out, binary.LittleEndian, uint32(16))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	_ = binary.Write(&out, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&out, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&out, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(&out, binary.LittleEndian, uint16(channels*bits/8))
	_ = binary.Write(&out, binary.LittleEndian, uint16(bits))
	out.WriteString("data")
	_ = binary.Write(&out, binary.LittleEndian, uint32(len(pcm)))
	out.Write(pcm)
	return out.Bytes()
}

// systemSpeaker is replaced in tests.
var systemSpeaker = runSystemSpeaker

// speakSystem uses the operating system voice: SAPI on Windows, say on macOS,
// espeak-ng or espeak on Linux. The text travels in a file, never on a
// command line, so it cannot be read as shell syntax.
func speakSystem(ctx context.Context, request SpeechRequest) (Speech, error) {
	dir, err := os.MkdirTemp("", "termixgo-tts-")
	if err != nil {
		return Speech{}, err
	}
	defer os.RemoveAll(dir)
	textPath := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(textPath, []byte(request.Text), 0o600); err != nil {
		return Speech{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	data, mime, ext, err := systemSpeaker(ctx, dir, textPath, request.Voice)
	if err != nil {
		return Speech{}, err
	}
	if len(data) == 0 {
		return Speech{}, errors.New("the system voice produced no audio")
	}
	return Speech{Data: data, MIME: mime, Ext: ext}, nil
}

func runSystemSpeaker(ctx context.Context, dir, textPath, voice string) ([]byte, string, string, error) {
	switch runtime.GOOS {
	case "windows":
		out := filepath.Join(dir, "speech.wav")
		script := filepath.Join(dir, "speak.ps1")
		body := `param([string]$In, [string]$Out, [string]$Voice)
Add-Type -AssemblyName System.Speech
$s = New-Object System.Speech.Synthesis.SpeechSynthesizer
if ($Voice) { try { $s.SelectVoice($Voice) } catch {} }
$s.SetOutputToWaveFile($Out)
$s.Speak([System.IO.File]::ReadAllText($In, [System.Text.Encoding]::UTF8))
$s.Dispose()
`
		if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
			return nil, "", "", err
		}
		cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-In", textPath, "-Out", out, "-Voice", voice)
		if output, err := cmd.CombinedOutput(); err != nil {
			return nil, "", "", fmt.Errorf("windows speech: %v %s", err, strings.TrimSpace(string(output)))
		}
		data, err := os.ReadFile(out)
		return data, "audio/wav", ".wav", err
	case "darwin":
		out := filepath.Join(dir, "speech.aiff")
		args := []string{"-f", textPath, "-o", out}
		if voice != "" {
			args = append(args, "-v", voice)
		}
		if output, err := exec.CommandContext(ctx, "say", args...).CombinedOutput(); err != nil {
			return nil, "", "", fmt.Errorf("say: %v %s", err, strings.TrimSpace(string(output)))
		}
		data, err := os.ReadFile(out)
		return data, "audio/aiff", ".aiff", err
	default:
		out := filepath.Join(dir, "speech.wav")
		for _, binary := range []string{"espeak-ng", "espeak"} {
			path, err := exec.LookPath(binary)
			if err != nil {
				continue
			}
			args := []string{"-f", textPath, "-w", out}
			if voice != "" {
				args = append(args, "-v", voice)
			}
			if output, err := exec.CommandContext(ctx, path, args...).CombinedOutput(); err != nil {
				return nil, "", "", fmt.Errorf("%s: %v %s", binary, err, strings.TrimSpace(string(output)))
			}
			data, err := os.ReadFile(out)
			return data, "audio/wav", ".wav", err
		}
		return nil, "", "", errors.New("no system voice: install espeak-ng, or add an OpenAI or Gemini key")
	}
}
