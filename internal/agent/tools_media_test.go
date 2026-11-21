package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/media"
)

func fakeImageServer(t *testing.T) {
	t.Helper()
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nimage"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/audio/speech") {
			_, _ = w.Write([]byte("ID3audio"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"b64_json": png}, map[string]any{"b64_json": png}}})
	}))
	t.Cleanup(server.Close)
	original := mediaCredentials
	mediaCredentials = func(*Env) media.Credentials {
		return func(id string) (string, string) {
			if id == "openai" {
				return "k", server.URL
			}
			return "", ""
		}
	}
	t.Cleanup(func() { mediaCredentials = original })
}

func TestGenerateImageSavesIntoTheWorkspace(t *testing.T) {
	fakeImageServer(t)
	env := &Env{Workspace: t.TempDir()}
	result, _ := (&generateImageTool{}).Run(context.Background(), env, map[string]any{"prompt": "a logo", "count": 2})
	if result.IsError || !strings.Contains(result.Output, "Generated 2 image(s)") {
		t.Fatalf("result = %+v", result)
	}
	files, _ := filepath.Glob(filepath.Join(env.Workspace, ".termixgo", "media", "image-*.png"))
	if len(files) != 2 {
		t.Fatalf("files = %v", files)
	}
}

func TestGenerateImageRefusesOverwriteAndGuests(t *testing.T) {
	fakeImageServer(t)
	env := &Env{Workspace: t.TempDir()}
	existing := filepath.Join(env.Workspace, "logo.png")
	if err := os.WriteFile(existing, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, _ := (&generateImageTool{}).Run(context.Background(), env, map[string]any{"prompt": "x", "output": "logo.png"})
	if !result.IsError || !strings.Contains(result.Output, "already exists") {
		t.Fatalf("an existing file must not be overwritten: %+v", result)
	}
	outside, _ := (&generateImageTool{}).Run(context.Background(), env, map[string]any{"prompt": "x", "output": filepath.Join(t.TempDir(), "x.png")})
	if !outside.IsError {
		t.Fatalf("an output outside the workspace must be refused")
	}
	guest := &Env{Workspace: env.Workspace, Guest: true}
	refused, _ := (&generateImageTool{}).Run(context.Background(), guest, map[string]any{"prompt": "x"})
	if !refused.IsError {
		t.Fatalf("a guest must not spend the operator's credits")
	}
}

func TestTextToSpeechSavesTheClip(t *testing.T) {
	fakeImageServer(t)
	env := &Env{Workspace: t.TempDir()}
	result, _ := (&textToSpeechTool{}).Run(context.Background(), env, map[string]any{"text": "halo", "output": "out/greeting"})
	if result.IsError || !strings.Contains(result.Output, "out/greeting.mp3") {
		t.Fatalf("result = %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(env.Workspace, "out", "greeting.mp3"))
	if err != nil || string(data) != "ID3audio" {
		t.Fatalf("clip = %q, %v", data, err)
	}
}
