package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/provider"
)

func pageServer(t *testing.T) string {
	t.Helper()
	t.Setenv("TERMIXGO_ALLOW_PRIVATE_FETCH", "1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><h1>Install</h1><p>"+strings.Repeat("Filler text. ", 400)+"</p><p>On Windows run winget install termixgo.</p></body></html>")
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestWebFetchWithPromptReturnsOnlyTheAnswer(t *testing.T) {
	url := pageServer(t)
	var gotPrompt, gotContent string
	env := &Env{Workspace: t.TempDir(), Extract: func(_ context.Context, _, prompt, content string) (string, error) {
		gotPrompt, gotContent = prompt, content
		return "Run `winget install termixgo`.", nil
	}}
	result, _ := (&webFetchTool{}).Run(context.Background(), env, map[string]any{"url": url, "prompt": "how to install on Windows"})
	if result.IsError || !strings.Contains(result.Output, "winget install termixgo") || strings.Contains(result.Output, "Filler text") {
		t.Fatalf("result = %q", result.Output)
	}
	if gotPrompt != "how to install on Windows" || !strings.Contains(gotContent, "Filler text") {
		t.Fatalf("the extractor should get the question and the page")
	}

	plain, _ := (&webFetchTool{}).Run(context.Background(), env, map[string]any{"url": url})
	if !strings.Contains(plain.Output, "Filler text") {
		t.Fatalf("without a prompt the page itself comes back")
	}
}

func TestWebFetchFallsBackToThePageWhenExtractionFails(t *testing.T) {
	url := pageServer(t)
	env := &Env{Workspace: t.TempDir(), Extract: func(context.Context, string, string, string) (string, error) {
		return "", errors.New("model offline")
	}}
	result, _ := (&webFetchTool{}).Run(context.Background(), env, map[string]any{"url": url, "prompt": "anything"})
	if result.IsError || !strings.Contains(result.Output, "extraction failed: model offline") || !strings.Contains(result.Output, "winget") {
		t.Fatalf("result = %q", result.Output)
	}
}

func TestExtractFromPageSendsTheQuestionAndClipsThePage(t *testing.T) {
	client := &fakeClient{steps: [][]provider.StreamEvent{{
		{Type: provider.EventTextDelta, Text: "the answer"},
		{Type: provider.EventUsage, Usage: &provider.Usage{PromptTokens: 900, CompletionTokens: 5, TotalTokens: 905}},
	}}}
	answer, usage, err := ExtractFromPage(context.Background(), client, "m", "", "https://x", "the limit?", strings.Repeat("y", extractPageLimit+5000))
	if err != nil || answer != "the answer" || usage.PromptTokens != 900 {
		t.Fatalf("answer=%q usage=%+v err=%v", answer, usage, err)
	}
	client.mu.Lock()
	request := client.requests[0]
	client.mu.Unlock()
	body := request.Messages[0].Content
	if !strings.Contains(body, "Question: the limit?") || !strings.Contains(body, "[page clipped]") || len(body) > extractPageLimit+500 {
		t.Fatalf("the request should carry the question and a clipped page (%d bytes)", len(body))
	}
	if request.System != ExtractSystemPrompt {
		t.Fatalf("the extraction system prompt should be used")
	}
}
