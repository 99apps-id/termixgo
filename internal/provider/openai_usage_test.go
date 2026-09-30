package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOpenAIStreamCountsRepeatedUsageOnce is the billing guard for
// OpenAI-compatible servers that repeat the request-wide usage on every chunk.
//
// StepFun's plan endpoint and several other compatible servers send the same
// cumulative counters on each chunk instead of only on the final one. The agent
// sums every usage event it is handed, so forwarding each chunk as-is charged
// the same tokens once per chunk: a real session recorded tens of millions of
// prompt tokens for a handful of steps and the cost ran far ahead of the
// provider's own dashboard. The client must emit only the increase.
func TestOpenAIStreamCountsRepeatedUsageOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"content":"Hel"}}],"usage":{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101}}`,
			`{"choices":[{"delta":{"content":"lo"}}],"usage":{"prompt_tokens":100,"completion_tokens":2,"total_tokens":102}}`,
			`{"choices":[{"delta":{"content":"!"}}],"usage":{"prompt_tokens":100,"completion_tokens":3,"total_tokens":103}}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":3,"total_tokens":103}}`,
			`[DONE]`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
	}))
	defer server.Close()

	client, err := newHTTPClient(Provider{ID: "stepfun-plan", Label: "StepFun Plan", Kind: KindOpenAI}, server.URL, "test-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	events := collect(t, client, ChatRequest{
		Model:    "step-3.7-flash",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})

	total := 0
	for _, event := range events {
		if event.Type == EventUsage && event.Usage != nil {
			total += event.Usage.TotalTokens
		}
	}
	if total != 103 {
		t.Errorf("usage total = %d, want the final cumulative value 103 counted once", total)
	}
}
