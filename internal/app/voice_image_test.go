package app

import (
	"context"
	"strings"
	"testing"
)

func TestAppVoiceAndImageModelConfiguration(t *testing.T) {
	application := newTestApp(t)

	// 1. VoiceModel
	if application.VoiceModel() != "" {
		t.Errorf("voice model initially empty, got %q", application.VoiceModel())
	}
	if err := application.SetVoiceModel("groq:whisper-large-v3-turbo"); err != nil {
		t.Fatalf("SetVoiceModel: %v", err)
	}
	if application.VoiceModel() != "groq:whisper-large-v3-turbo" {
		t.Errorf("expected groq:whisper-large-v3-turbo, got %q", application.VoiceModel())
	}

	// 2. ImageModel
	if application.ImageModel() != "" {
		t.Errorf("image model initially empty, got %q", application.ImageModel())
	}
	if err := application.SetImageModel("openai:dall-e-3"); err != nil {
		t.Fatalf("SetImageModel: %v", err)
	}
	if application.ImageModel() != "openai:dall-e-3" {
		t.Errorf("expected openai:dall-e-3, got %q", application.ImageModel())
	}
	if application.cfg.SubagentModels["image"] != "openai:dall-e-3" {
		t.Errorf("expected subagent role image to map to dall-e-3, got %q", application.cfg.SubagentModels["image"])
	}

	// 3. Clear models
	if err := application.SetVoiceModel(""); err != nil {
		t.Fatalf("clear voice model: %v", err)
	}
	if application.VoiceModel() != "" {
		t.Errorf("expected empty voice model, got %q", application.VoiceModel())
	}

	if err := application.SetImageModel(""); err != nil {
		t.Fatalf("clear image model: %v", err)
	}
	if application.ImageModel() != "" {
		t.Errorf("expected empty image model, got %q", application.ImageModel())
	}
	if _, ok := application.cfg.SubagentModels["image"]; ok {
		t.Errorf("subagent role image should be deleted when image model is cleared")
	}
}

func TestAppRunVoicePromptFallback(t *testing.T) {
	application := newTestApp(t)

	// With no model configured and no voice model, RunVoicePrompt returns error
	_, err := application.RunVoicePrompt(context.Background(), "hello voice", nil)
	if err == nil {
		t.Fatalf("expected error when no model configured")
	}

	// Configure voice fallback model
	_ = application.SetVoiceModel("openai:gpt-4o")

	// Even if it fails, verify fallback message was sent to progress if provided
	var progressLines []string
	_, err = application.RunVoicePrompt(context.Background(), "hello voice", func(line string) {
		progressLines = append(progressLines, line)
	})
	if err == nil {
		t.Fatalf("expected error since mock client is unavailable")
	}

	foundFallbackNote := false
	for _, l := range progressLines {
		if strings.Contains(l, "Falling back to voice model") {
			foundFallbackNote = true
			break
		}
	}
	if !foundFallbackNote {
		t.Errorf("progress should record fallback to voice model, got: %v", progressLines)
	}
}
