package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestOllamaCloudStream(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("OLLAMA_API_KEY"))
	if key == "" {
		t.Skip("OLLAMA_API_KEY not set; export it to run the cloud test")
	}
	baseURL := strings.TrimSpace(os.Getenv("OLLAMA_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://ollama.com/v1"
	}
	info, ok := ByID("ollama")
	if !ok {
		t.Fatal("ollama provider not found in registry")
	}
	client, err := newHTTPClient(info, baseURL, key)
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	if setter, ok := client.(interface{ SetKeyResolver(KeyResolver) }); ok {
		setter.SetKeyResolver(nil)
	}

	var text string
	streamErr := client.Stream(context.Background(), ChatRequest{
		Model:    "gemma4:31b",
		Messages: []Message{{Role: RoleUser, Content: "Say hello in exactly 3 words."}},
	}, func(event StreamEvent) error {
		if event.Type == EventTextDelta {
			text += event.Text
		}
		return nil
	})
	if streamErr != nil {
		t.Fatalf("Stream: %v", streamErr)
	}
	fmt.Printf("OLLAMA_CLOUD_REPLY: %s\n", strings.TrimSpace(text))
}
