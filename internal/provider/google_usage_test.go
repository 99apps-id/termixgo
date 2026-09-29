package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sumUsageEvents totals the usage events a client emitted, which is exactly what
// the agent loop does with them.
func sumUsageEvents(events []StreamEvent) Usage {
	var total Usage
	for _, event := range events {
		if event.Type == EventUsage && event.Usage != nil {
			total = total.Add(*event.Usage)
		}
	}
	return total
}

// googleUsageServer streams the given raw JSON chunks as SSE.
func googleUsageServer(t *testing.T, chunks []string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// TestGoogleUsageIsNotCountedOncePerChunk is the double-count guard.
//
// Gemini repeats usageMetadata on every chunk and its counters cover the whole
// request, so the value climbs as the answer streams. The agent sums every usage
// event it is handed, so forwarding each chunk as-is charged the same tokens once
// per chunk: the token count and the estimated spend ran several times too high
// and a cost budget tripped early.
func TestGoogleUsageIsNotCountedOncePerChunk(t *testing.T) {
	url := googleUsageServer(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"Hello"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}}`,
		`{"candidates":[{"content":{"parts":[{"text":" world"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`,
		`{"candidates":[{"content":{"parts":[{"text":"!"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":6,"totalTokenCount":16}}`,
	})
	client, _ := newHTTPClient(Provider{ID: "google", Label: "Google", Kind: KindGoogle}, url, "google-key")
	events := collect(t, client, ChatRequest{Model: "gemini-3-pro", Messages: []Message{{Role: RoleUser, Content: "hi"}}})

	want := Usage{PromptTokens: 10, CompletionTokens: 6, TotalTokens: 16}
	if got := sumUsageEvents(events); got != want {
		t.Errorf("summed usage = %+v, want the final cumulative %+v", got, want)
	}
}

// TestGoogleUsageFromASingleFinalChunk is the shape the providers really send at
// the end of a stream, and pins that the change did not drop it.
func TestGoogleUsageFromASingleFinalChunk(t *testing.T) {
	url := googleUsageServer(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"Answer"}]}}]}`,
		`{"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7}}`,
	})
	client, _ := newHTTPClient(Provider{ID: "google", Label: "Google", Kind: KindGoogle}, url, "google-key")
	events := collect(t, client, ChatRequest{Model: "gemini-3-pro", Messages: []Message{{Role: RoleUser, Content: "hi"}}})

	want := Usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}
	if got := sumUsageEvents(events); got != want {
		t.Errorf("summed usage = %+v, want %+v", got, want)
	}
}

// TestGoogleUsageIgnoresARepeatedReport covers a stream that repeats the same
// final usage: the ground is already charged, so it must not be added again.
func TestGoogleUsageIgnoresARepeatedReport(t *testing.T) {
	url := googleUsageServer(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"Answer"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":5,"totalTokenCount":10}}`,
		`{"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":5,"totalTokenCount":10}}`,
	})
	client, _ := newHTTPClient(Provider{ID: "google", Label: "Google", Kind: KindGoogle}, url, "google-key")
	events := collect(t, client, ChatRequest{Model: "gemini-3-pro", Messages: []Message{{Role: RoleUser, Content: "hi"}}})

	want := Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10}
	if got := sumUsageEvents(events); got != want {
		t.Errorf("summed usage = %+v, want %+v", got, want)
	}
}
