package provider

import "testing"

// TestEffortReachesTheWirePayload is the end of the chain the mapping tests
// only cover in the middle: the level has to appear in the body each client
// actually posts, under that vendor's own field name.
func TestEffortReachesTheWirePayload(t *testing.T) {
	messages := []Message{{Role: RoleUser, Content: "think"}}

	t.Run("openai uses reasoning_effort", func(t *testing.T) {
		recorder, server := newPayloadRecorder(t)
		defer server.Close()
		client, err := newHTTPClient(Provider{ID: "openai", Label: "OpenAI", Kind: KindOpenAI}, server.URL, "key")
		if err != nil {
			t.Fatalf("newHTTPClient: %v", err)
		}
		collect(t, client, ChatRequest{Model: "gpt-5.4", Messages: messages, Effort: EffortMedium})
		if got := recorder.payload(t)["reasoning_effort"]; got != "medium" {
			t.Errorf("reasoning_effort = %v, want medium", got)
		}
	})

	t.Run("anthropic uses output_config", func(t *testing.T) {
		recorder, server := newPayloadRecorder(t)
		defer server.Close()
		client, err := newHTTPClient(Provider{ID: "anthropic", Label: "Anthropic", Kind: KindAnthropic}, server.URL, "key")
		if err != nil {
			t.Fatalf("newHTTPClient: %v", err)
		}
		collect(t, client, ChatRequest{Model: "claude-sonnet-4-5", Messages: messages, Effort: EffortMax})
		output := object(t, recorder.payload(t)["output_config"], "output_config")
		if output["effort"] != "max" {
			t.Errorf("output_config.effort = %v, want max", output["effort"])
		}
	})

	t.Run("google uses a thinking level", func(t *testing.T) {
		recorder, server := newPayloadRecorder(t)
		defer server.Close()
		client, err := newHTTPClient(Provider{ID: "google", Label: "Google", Kind: KindGoogle}, server.URL, "key")
		if err != nil {
			t.Fatalf("newHTTPClient: %v", err)
		}
		collect(t, client, ChatRequest{Model: "gemini-3-flash", Messages: messages, Effort: EffortLow})
		generation := object(t, recorder.payload(t)["generationConfig"], "generationConfig")
		thinking := object(t, generation["thinkingConfig"], "thinkingConfig")
		if thinking["thinkingLevel"] != "low" {
			t.Errorf("thinkingLevel = %v, want low", thinking["thinkingLevel"])
		}
	})
}

// TestNoEffortLeavesTheWireAlone is the safety half: with no level chosen the
// vendor's own default must apply, which means the field is absent rather than
// set to a guess. An unknown field here is a 400, not a hint.
func TestNoEffortLeavesTheWireAlone(t *testing.T) {
	cases := []struct {
		name     string
		provider Provider
		model    string
		absent   string
	}{
		{"openai", Provider{ID: "openai", Label: "OpenAI", Kind: KindOpenAI}, "gpt-5.4", "reasoning_effort"},
		{"anthropic", Provider{ID: "anthropic", Label: "Anthropic", Kind: KindAnthropic}, "claude-sonnet-4-5", "output_config"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder, server := newPayloadRecorder(t)
			defer server.Close()
			client, err := newHTTPClient(testCase.provider, server.URL, "key")
			if err != nil {
				t.Fatalf("newHTTPClient: %v", err)
			}
			collect(t, client, ChatRequest{Model: testCase.model, Messages: []Message{{Role: RoleUser, Content: "hi"}}})
			if _, present := recorder.payload(t)[testCase.absent]; present {
				t.Errorf("%s should be absent when no effort is set", testCase.absent)
			}
		})
	}
}
