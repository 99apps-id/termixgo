package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ImageRequest asks for one or more images.
type ImageRequest struct {
	Prompt string
	// Model is "provider:model", a bare model of the first available
	// provider, or empty for the default order.
	Model string
	// Size is WIDTHxHEIGHT; empty lets the provider choose.
	Size  string
	Count int
}

// Image is one generated picture.
type Image struct {
	Data []byte
	MIME string
}

// Ext is the file extension that matches the image type.
func (i Image) Ext() string {
	switch strings.ToLower(i.MIME) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

// imageBackends is the default order. Each entry names the provider and its
// image model; the first provider with a key is used.
var imageBackends = []struct{ provider, model string }{
	{"openai", "gpt-image-1"},
	{"google", "gemini-2.5-flash-image"},
	{"xai", "grok-2-image"},
}

// IsImageGenerationModel reports whether a model id names an image
// generation model rather than a chat or vision model.
func IsImageGenerationModel(spec string) bool {
	_, model := splitModel(spec)
	lower := strings.ToLower(model)
	for _, marker := range []string{"dall-e", "gpt-image", "imagen", "-image", "flux", "stable-diffusion", "sdxl"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// imageProviderFor infers the provider of a bare image model id.
func imageProviderFor(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "dall-e"), strings.Contains(lower, "gpt-image"):
		return "openai"
	case strings.Contains(lower, "gemini"), strings.Contains(lower, "imagen"):
		return "google"
	case strings.Contains(lower, "grok"):
		return "xai"
	}
	return ""
}

// ErrNoImageProvider means no provider with image generation has a key.
var ErrNoImageProvider = errors.New("no image-capable provider is configured: add an OpenAI, Google Gemini or xAI key (termixgo secret openai|google|xai), or set imageModel")

// GenerateImage produces images, trying the requested model first and then
// the default order. It returns the images and the "provider:model" used.
func GenerateImage(ctx context.Context, request ImageRequest, credentials Credentials) ([]Image, string, error) {
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, "", errors.New("prompt is required")
	}
	if request.Count < 1 {
		request.Count = 1
	}
	if request.Count > 4 {
		request.Count = 4
	}
	wantProvider, wantModel := splitModel(request.Model)
	if wantProvider == "" && wantModel != "" {
		wantProvider = imageProviderFor(wantModel)
	}
	var failures []string
	tried := false
	for _, backend := range imageBackends {
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
		tried = true
		images, err := generateWith(ctx, backend.provider, model, key, base, request)
		if err == nil && len(images) > 0 {
			return images, backend.provider + ":" + model, nil
		}
		if err == nil {
			err = errors.New("the provider returned no image")
		}
		failures = append(failures, fmt.Sprintf("%s:%s: %v", backend.provider, model, err))
		if wantProvider != "" {
			break
		}
	}
	if !tried {
		if wantProvider != "" {
			return nil, "", fmt.Errorf("%s has no key or does not generate images", wantProvider)
		}
		return nil, "", ErrNoImageProvider
	}
	return nil, "", fmt.Errorf("image generation failed: %s", strings.Join(failures, "; "))
}

func generateWith(ctx context.Context, providerID, model, key, base string, request ImageRequest) ([]Image, error) {
	switch providerID {
	case "google":
		if strings.Contains(strings.ToLower(model), "imagen") {
			return generateImagen(ctx, model, key, base, request)
		}
		return generateGemini(ctx, model, key, base, request)
	default:
		return generateOpenAIStyle(ctx, providerID, model, key, base, request)
	}
}

// generateOpenAIStyle calls /images/generations, which OpenAI and xAI share.
func generateOpenAIStyle(ctx context.Context, providerID, model, key, base string, request ImageRequest) ([]Image, error) {
	payload := map[string]any{"model": model, "prompt": request.Prompt, "n": request.Count}
	// gpt-image models always answer in base64; the older models and xAI
	// need to be asked, or they answer with a short-lived URL.
	if !strings.HasPrefix(model, "gpt-image") {
		payload["response_format"] = "b64_json"
	}
	// xAI rejects a size; OpenAI accepts the sizes its models list.
	if request.Size != "" && providerID != "xai" {
		payload["size"] = request.Size
	}
	data, err := postJSON(ctx, strings.TrimRight(base, "/")+"/images/generations", bearer(key), payload)
	if err != nil {
		return nil, err
	}
	var answer struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
		OutputFormat string `json:"output_format"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return nil, fmt.Errorf("unreadable answer: %w", err)
	}
	mime := "image/png"
	switch strings.ToLower(answer.OutputFormat) {
	case "jpeg", "jpg":
		mime = "image/jpeg"
	case "webp":
		mime = "image/webp"
	}
	var images []Image
	for _, item := range answer.Data {
		raw, err := base64.StdEncoding.DecodeString(item.B64JSON)
		if err != nil || len(raw) == 0 {
			continue
		}
		images = append(images, Image{Data: raw, MIME: sniffImage(raw, mime)})
	}
	return images, nil
}

// generateGemini calls a Gemini image model. It answers one image per call,
// so a count above one makes several calls.
func generateGemini(ctx context.Context, model, key, base string, request ImageRequest) ([]Image, error) {
	prompt := request.Prompt
	if request.Size != "" {
		prompt += "\n\nImage size: " + request.Size
	}
	payload := map[string]any{
		"contents":         []any{map[string]any{"parts": []any{map[string]any{"text": prompt}}}},
		"generationConfig": map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}},
	}
	var images []Image
	var lastText string
	for len(images) < request.Count {
		data, err := postJSON(ctx, googleModelURL(base, model), googleHeaders(key), payload)
		if err != nil {
			if len(images) > 0 {
				return images, nil
			}
			return nil, err
		}
		parts, text, err := googleInlineParts(data)
		if err != nil {
			return nil, err
		}
		lastText = text
		added := 0
		for _, part := range parts {
			if !strings.HasPrefix(part.MimeType, "image/") {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(part.Data)
			if err != nil || len(raw) == 0 {
				continue
			}
			images = append(images, Image{Data: raw, MIME: sniffImage(raw, part.MimeType)})
			added++
		}
		if added == 0 {
			break
		}
	}
	if len(images) == 0 && lastText != "" {
		return nil, fmt.Errorf("the model answered with text instead of an image: %s", shorten(lastText, 200))
	}
	return images, nil
}

// generateImagen calls an Imagen model, which uses the predict endpoint.
func generateImagen(ctx context.Context, model, key, base string, request ImageRequest) ([]Image, error) {
	parameters := map[string]any{"sampleCount": request.Count}
	if ratio := aspectRatio(request.Size); ratio != "" {
		parameters["aspectRatio"] = ratio
	}
	url := strings.TrimSuffix(googleModelURL(base, model), ":generateContent") + ":predict"
	data, err := postJSON(ctx, url, googleHeaders(key), map[string]any{
		"instances":  []any{map[string]any{"prompt": request.Prompt}},
		"parameters": parameters,
	})
	if err != nil {
		return nil, err
	}
	var answer struct {
		Predictions []struct {
			Bytes    string `json:"bytesBase64Encoded"`
			MimeType string `json:"mimeType"`
		} `json:"predictions"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return nil, fmt.Errorf("unreadable Imagen answer: %w", err)
	}
	var images []Image
	for _, prediction := range answer.Predictions {
		raw, err := base64.StdEncoding.DecodeString(prediction.Bytes)
		if err != nil || len(raw) == 0 {
			continue
		}
		images = append(images, Image{Data: raw, MIME: sniffImage(raw, prediction.MimeType)})
	}
	return images, nil
}

// aspectRatio maps WIDTHxHEIGHT onto the ratios Imagen accepts.
func aspectRatio(size string) string {
	width, height, ok := strings.Cut(strings.ToLower(strings.TrimSpace(size)), "x")
	if !ok {
		return ""
	}
	w, errW := strconv.Atoi(width)
	h, errH := strconv.Atoi(height)
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return ""
	}
	ratio := float64(w) / float64(h)
	best, bestDiff := "1:1", 10.0
	for _, candidate := range []struct {
		name  string
		value float64
	}{{"1:1", 1}, {"4:3", 4.0 / 3}, {"3:4", 3.0 / 4}, {"16:9", 16.0 / 9}, {"9:16", 9.0 / 16}} {
		diff := ratio - candidate.value
		if diff < 0 {
			diff = -diff
		}
		if diff < bestDiff {
			best, bestDiff = candidate.name, diff
		}
	}
	return best
}

// sniffImage trusts the bytes over the declared type.
func sniffImage(data []byte, declared string) string {
	switch {
	case len(data) > 8 && string(data[1:4]) == "PNG":
		return "image/png"
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8:
		return "image/jpeg"
	case len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	}
	if declared == "" {
		return "image/png"
	}
	return declared
}

func shorten(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
