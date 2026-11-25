package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/media"
	"github.com/99apps-id/termixgo/internal/provider"
)

// mediaCredentials resolves provider keys for the media backends. It is a
// variable so tests can point the backends at a local server.
var mediaCredentials = func(env *Env) media.Credentials {
	return func(id string) (string, string) {
		return provider.ResolveKey(env.Secrets, id), provider.BaseURLFor(env.Config, id)
	}
}

// mediaOutputPath picks where a generated file is written. An explicit path
// must stay in the workspace and must not overwrite an existing file; the
// default is .termixgo/media with a timestamped name.
func mediaOutputPath(env *Env, requested, prefix, ext string, index int) (string, error) {
	if strings.TrimSpace(requested) != "" {
		path := resolvePath(env, requested)
		if filepath.Ext(path) == "" {
			path += ext
		}
		if index > 0 {
			path = strings.TrimSuffix(path, filepath.Ext(path)) + fmt.Sprintf("-%d", index+1) + filepath.Ext(path)
		}
		if err := checkWorkspacePath(env, path); err != nil {
			return "", err
		}
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("%s already exists; choose another output path", displayPath(env, path))
		}
		return path, nil
	}
	dir := filepath.Join(env.Workspace, ".termixgo", "media")
	name := fmt.Sprintf("%s-%s", prefix, time.Now().Format("20060102-150405"))
	if index > 0 {
		name += fmt.Sprintf("-%d", index+1)
	}
	return filepath.Join(dir, name+ext), nil
}

func writeMedia(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// mediaGuard refuses the media tools where they would spend the operator's
// credits on someone else's request, or have nowhere to write.
func mediaGuard(env *Env) (Result, bool) {
	if env == nil || strings.TrimSpace(env.Workspace) == "" {
		return Result{Output: "no workspace to save the file in", IsError: true}, false
	}
	if env.Guest {
		return Result{Output: "generating images and speech is available to the operator only", IsError: true}, false
	}
	return Result{}, true
}

// ---------------------------------------------------------------- image

type generateImageTool struct{}

func (t *generateImageTool) Name() string { return "generate_image" }
func (t *generateImageTool) Aliases() []string {
	return []string{"image_generate", "create_image", "text_to_image"}
}
func (t *generateImageTool) Mutating() bool { return false }
func (t *generateImageTool) Risk() Risk     { return RiskNetwork }
func (t *generateImageTool) Label(a map[string]any) string {
	return "Generating image: " + Shorten(argString(a, "prompt"), 50)
}
func (t *generateImageTool) DoneLabel(a map[string]any) string {
	return "Generated image: " + Shorten(argString(a, "prompt"), 50)
}
func (t *generateImageTool) Description() string {
	return "Generate an image from a text description with an image model (OpenAI gpt-image-1, Google Gemini image, or xAI Grok image, whichever has a key; config imageModel picks one). Saves PNG files in the workspace and returns their paths, which read_image can view and send_channel can deliver. Costs provider credits, so call it once per request with a detailed prompt."
}
func (t *generateImageTool) Schema() map[string]any {
	return object(map[string]any{
		"prompt": strProp("Detailed description: subject, style, composition, colours, text to render."),
		"size":   strProp("Optional WIDTHxHEIGHT, for example 1024x1024, 1536x1024 or 1024x1536."),
		"count":  intProp("Number of images, 1 to 4. Defaults to 1."),
		"model":  strProp("Optional provider:model override, for example openai:gpt-image-1 or google:gemini-2.5-flash-image."),
		"output": strProp("Optional workspace path for the file. Defaults to .termixgo/media/."),
	}, "prompt")
}

func (t *generateImageTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if refusal, ok := mediaGuard(env); !ok {
		return refusal, nil
	}
	model := strings.TrimSpace(argString(args, "model"))
	if model == "" && media.IsImageGenerationModel(env.Config.ImageModel) {
		// imageModel also names the vision model of the image subagent, so
		// only a generation model is taken from it.
		model = env.Config.ImageModel
	}
	images, used, err := media.GenerateImage(ctx, media.ImageRequest{
		Prompt: argString(args, "prompt"),
		Model:  model,
		Size:   strings.TrimSpace(argString(args, "size")),
		Count:  argInt(args, "count", 1, 1, 4),
	}, mediaCredentials(env))
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	var saved []string
	for index, image := range images {
		path, err := mediaOutputPath(env, argString(args, "output", "path"), "image", image.Ext(), index)
		if err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		if err := writeMedia(path, image.Data); err != nil {
			return Result{Output: fmt.Sprintf("save %s: %v", displayPath(env, path), err), IsError: true}, nil
		}
		saved = append(saved, fmt.Sprintf("%s (%d KB)", displayPath(env, path), (len(image.Data)+1023)/1024))
	}
	return Result{
		Output:  fmt.Sprintf("Generated %d image(s) with %s:\n%s", len(saved), used, strings.Join(saved, "\n")),
		Display: fmt.Sprintf("%d image(s) with %s", len(saved), used),
	}, nil
}

// ---------------------------------------------------------------- speech

type textToSpeechTool struct{}

func (t *textToSpeechTool) Name() string      { return "text_to_speech" }
func (t *textToSpeechTool) Aliases() []string { return []string{"tts", "speak", "generate_speech"} }
func (t *textToSpeechTool) Mutating() bool    { return false }
func (t *textToSpeechTool) Risk() Risk        { return RiskNetwork }
func (t *textToSpeechTool) Label(a map[string]any) string {
	return "Speaking: " + Shorten(argString(a, "text"), 50)
}
func (t *textToSpeechTool) DoneLabel(a map[string]any) string {
	return "Spoke: " + Shorten(argString(a, "text"), 50)
}
func (t *textToSpeechTool) Description() string {
	return "Turn text into a spoken audio file (OpenAI gpt-4o-mini-tts, Gemini TTS, ElevenLabs when named explicitly, or the operating system voice when no key is set; config speechModel picks one). Saves the file in the workspace and returns its path, which send_channel can deliver as audio. Up to 4096 characters per call."
}
func (t *textToSpeechTool) Schema() map[string]any {
	return object(map[string]any{
		"text":         strProp("What to say, up to 4096 characters."),
		"voice":        strProp("Optional voice: alloy, nova, shimmer, onyx (OpenAI); Kore, Puck, Charon (Gemini); a voice id (ElevenLabs); an installed voice name for the system voice."),
		"format":       strProp("mp3 (default), opus (best for chat voice notes), wav or aac. Gemini and the system voice save wav."),
		"instructions": strProp("Optional delivery: tone, pace, accent, for example 'warm and slow, Indonesian accent'."),
		"model":        strProp("Optional provider:model override, or system for the local voice. ElevenLabs runs only when named here or in config speechModel, for example elevenlabs:eleven_multilingual_v2; it is never a silent fallback because it bills per character."),
		"output":       strProp("Optional workspace path for the file. Defaults to .termixgo/media/."),
	}, "text")
}

func (t *textToSpeechTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if refusal, ok := mediaGuard(env); !ok {
		return refusal, nil
	}
	model := strings.TrimSpace(argString(args, "model"))
	if model == "" {
		model = env.Config.SpeechModel
	}
	clip, used, err := media.Speak(ctx, media.SpeechRequest{
		Text:         argString(args, "text"),
		Model:        model,
		Voice:        argString(args, "voice"),
		Format:       argString(args, "format"),
		Instructions: argString(args, "instructions"),
	}, mediaCredentials(env))
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	path, err := mediaOutputPath(env, argString(args, "output", "path"), "speech", clip.Ext, 0)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if err := writeMedia(path, clip.Data); err != nil {
		return Result{Output: fmt.Sprintf("save %s: %v", displayPath(env, path), err), IsError: true}, nil
	}
	return Result{
		Output:  fmt.Sprintf("Saved speech with %s: %s (%d KB, %s)", used, displayPath(env, path), (len(clip.Data)+1023)/1024, clip.MIME),
		Display: fmt.Sprintf("speech with %s", used),
	}, nil
}
