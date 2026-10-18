package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// streamFrames serves the given frames as one SSE body.
func streamFrames(frames ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range frames {
			fmt.Fprintf(w, "data: %s\n\n", frame)
		}
	}
}

func openAIClientFor(t *testing.T, handler http.HandlerFunc) Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := newHTTPClient(Provider{ID: "openai", Label: "OpenAI"}, server.URL, "test-key")
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	return client
}

// TestOpenAIStreamFailsOnAnInBandError is the gateway-error guard.
//
// Several OpenAI-compatible hosts report a failure inside a 200 response as a
// stream frame rather than as a status code. That frame carries no choices, so
// decoding it into a chunk that had no error member made the stream look like a
// clean end: the agent got a half-finished answer and a nil error, and the
// operator read a rejected request as the model choosing to stop early.
func TestOpenAIStreamFailsOnAnInBandError(t *testing.T) {
	tests := []struct {
		name    string
		frame   string
		needle  string
		wantErr bool
	}{
		{
			name:   "object form",
			frame:  `{"error":{"message":"context_length_exceeded","type":"invalid_request_error"}}`,
			needle: "context_length_exceeded",
		},
		{
			name:   "bare string form",
			frame:  `{"error":"upstream rejected the request"}`,
			needle: "upstream rejected the request",
		},
		{
			name:   "object with a numeric code",
			frame:  `{"error":{"message":"slow down","code":429}}`,
			needle: "slow down",
		},
		{
			name:  "an explicit null is not an error",
			frame: `{"choices":[{"delta":{"content":"fine"}}],"error":null}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := openAIClientFor(t, streamFrames(
				`{"choices":[{"delta":{"content":"partial answer"}}]}`,
				test.frame,
				`[DONE]`,
			))
			var text string
			err := client.Stream(context.Background(), ChatRequest{Model: "gpt-5.4"}, func(event StreamEvent) error {
				if event.Type == EventTextDelta {
					text += event.Text
				}
				return nil
			})
			if test.needle == "" {
				if err != nil {
					t.Fatalf("a frame that reports no error must not fail the stream: %v", err)
				}
				// The opening frame and the null-error frame both carry text, so
				// the whole answer has to survive.
				if text != "partial answerfine" {
					t.Errorf("text = %q, want the streamed answer to survive", text)
				}
				return
			}
			if err == nil {
				t.Fatalf("the stream reported success with text %q, want the frame error surfaced", text)
			}
			if !strings.Contains(err.Error(), test.needle) {
				t.Errorf("error = %q, want it to name %q", err.Error(), test.needle)
			}
		})
	}
}

// TestOpenAIStreamStillToleratesUnreadableFrames guards the other side of the
// fix: a keep-alive or a field this client does not model must stay harmless,
// or closing the error hole would break the servers that work today.
func TestOpenAIStreamStillToleratesUnreadableFrames(t *testing.T) {
	client := openAIClientFor(t, streamFrames(
		`{"choices":[{"delta":{"content":"ok"}}]}`,
		`not json at all`,
		`{"choices":[{"delta":{"content":[]}}]}`,
		`[DONE]`,
	))
	var text string
	if err := client.Stream(context.Background(), ChatRequest{Model: "gpt-5.4"}, func(event StreamEvent) error {
		if event.Type == EventTextDelta {
			text += event.Text
		}
		return nil
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if text != "ok" {
		t.Errorf("text = %q, want the frames that did parse", text)
	}
}
