package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/99apps-id/termixgo/internal/provider"
)

// maxImageBytes bounds one attachment so a huge file cannot flood the request.
const maxImageBytes = 8 * 1024 * 1024

// imageMediaTypes maps a file extension to the media type a provider expects.
var imageMediaTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
}

// readImageTool attaches a local image to the conversation for a vision model.
type readImageTool struct{}

func (t *readImageTool) Name() string      { return "read_image" }
func (t *readImageTool) Aliases() []string { return []string{"view_image", "read_image_file"} }
func (t *readImageTool) Mutating() bool    { return false }
func (t *readImageTool) Risk() Risk        { return RiskEdit }
func (t *readImageTool) Label(a map[string]any) string {
	return "Reading image " + displayName(a)
}
func (t *readImageTool) DoneLabel(a map[string]any) string {
	return "Read image " + displayName(a)
}
func (t *readImageTool) Description() string {
	return "Read a local image (png, jpg, gif, webp, bmp) and attach it so a vision model can see it: a screenshot, mockup, diagram or chart. Use read_file for text."
}
func (t *readImageTool) Schema() map[string]any {
	return object(map[string]any{
		"path": strProp("Image path, absolute or relative to the workspace."),
	}, "path")
}

func (t *readImageTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	raw := strings.TrimSpace(argString(args, "path"))
	if raw == "" {
		return Result{Output: "path is required", IsError: true}, nil
	}
	path := resolvePath(env, raw)
	if err := checkWorkspacePath(env, path); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	mediaType, ok := imageMediaTypes[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return Result{Output: fmt.Sprintf("%s is not a supported image type (png, jpg, gif, webp, bmp)", displayPath(env, path)), IsError: true}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return Result{Output: openError(err, displayPath(env, path)), IsError: true}, nil
	}
	if info.IsDir() {
		return Result{Output: displayPath(env, path) + " is a directory", IsError: true}, nil
	}
	if info.Size() > maxImageBytes {
		return Result{Output: fmt.Sprintf("%s is %d bytes, over the %d byte image cap", displayPath(env, path), info.Size(), maxImageBytes), IsError: true}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Output: openError(err, displayPath(env, path)), IsError: true}, nil
	}
	// A screenshot can be taller than a provider accepts. Scale it down at
	// attach time so the oversized bytes never enter the session, where they
	// would be resent and rejected on every later turn.
	note := ""
	if shrunk, changed := ShrinkImage(data); changed {
		data = shrunk
		mediaType = "image/png"
		note = " (scaled down to fit the image size limit)"
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return Result{
		Output: fmt.Sprintf("Attached image %s (%s, %d bytes)%s. Describe what you see.", displayPath(env, path), mediaType, len(data), note),
		Images: []provider.Image{{MediaType: mediaType, Data: encoded}},
	}, nil
}
