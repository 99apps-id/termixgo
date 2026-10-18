package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

// ------------------------------------------------- SSE parsing

// TestSSEReaderHandlesTheFramingRules walks the parsing rules that a real
// provider stream exercises: keep-alive comments, a multi-line data payload,
// and a final event that arrives without a trailing blank line.
func TestSSEReaderHandlesTheFramingRules(t *testing.T) {
	stream := strings.Join([]string{
		": keep-alive",
		"",
		"event: message",
		"data: {\"a\":",
		"data: 1}",
		"",
		"data: {\"b\":2}",
		"",
		"data: {\"c\":3}",
	}, "\n")
	reader := newSSEReader(strings.NewReader(stream))

	first, err := reader.next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	// The comment is skipped and the two data lines are joined with a newline,
	// which is the SSE rule for a multi-line payload.
	if first != "{\"a\":\n1}" {
		t.Errorf("first = %q, want the joined data", first)
	}

	second, err := reader.next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if second != "{\"b\":2}" {
		t.Errorf("second = %q", second)
	}

	// A payload at the end of the stream with no terminating blank line is
	// still delivered, because the last chunk of a response often looks like
	// this.
	third, err := reader.next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if third != "{\"c\":3}" {
		t.Errorf("third = %q, want the unterminated tail", third)
	}

	if _, err := reader.next(); !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want io.EOF at the end", err)
	}
}

// TestSSEReaderSkipsBlankPreamble covers a stream that opens with blank lines,
// which some proxies add.
func TestSSEReaderSkipsBlankPreamble(t *testing.T) {
	reader := newSSEReader(strings.NewReader("\n\n\ndata: {\"x\":1}\n\n"))
	payload, err := reader.next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if payload != "{\"x\":1}" {
		t.Errorf("payload = %q", payload)
	}
}

// TestSSEReaderSurfacesAReadFailure keeps a broken connection from ending the
// stream as a clean response with no answer.
func TestSSEReaderSurfacesAReadFailure(t *testing.T) {
	reader := newSSEReader(&failingReader{})
	if _, err := reader.next(); err == nil {
		t.Fatalf("a read failure must be reported, not treated as the end")
	}
}

// failingReader returns an error instead of a body.
type failingReader struct{}

func (f *failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset by peer") }

// ------------------------------------------------- status errors

// TestStatusErrorReadsTheProvidersOwnMessage is what turns a 400 into something
// the operator can act on instead of "returned 400: ".
func TestStatusErrorReadsTheProvidersOwnMessage(t *testing.T) {
	client := &httpClient{info: Provider{Label: "Anthropic"}}

	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"nested error object", 400, `{"error":{"message":"input too long","type":"invalid_request"}}`, "input too long"},
		{"flat message", 500, `{"message":"upstream is down"}`, "upstream is down"},
		{"plain text body", 502, "bad gateway from the proxy", "bad gateway from the proxy"},
		{"empty body falls back to the status text", 503, "", "Service Unavailable"},
		{"unauthorized names the fix", 401, `{"error":{"message":"no key"}}`, "check the API key"},
		{"forbidden names the fix", 403, "", "check the API key"},
		{"rate limit names the fix", 429, "", "rate limited"},
		{"not found names the fix", 404, "", "check the model id"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := &http.Response{
				StatusCode: testCase.status,
				Body:       io.NopCloser(strings.NewReader(testCase.body)),
				Header:     http.Header{},
			}
			err := client.statusError(response)
			if err == nil {
				t.Fatalf("no error was built")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("err = %q, want it to contain %q", err, testCase.want)
			}
			if !strings.Contains(err.Error(), "Anthropic") {
				t.Errorf("err = %q, want the provider label", err)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%d", testCase.status)) {
				t.Errorf("err = %q, want the status code", err)
			}
		})
	}
}

func TestStatusErrorClipsAVeryLongMessage(t *testing.T) {
	client := &httpClient{info: Provider{Label: "OpenAI"}}
	response := &http.Response{
		StatusCode: 400,
		Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", 5000))),
		Header:     http.Header{},
	}
	err := client.statusError(response)
	if err == nil {
		t.Fatalf("no error was built")
	}
	if len(err.Error()) > 700 {
		t.Errorf("err is %d chars, want the body clipped", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "...") {
		t.Errorf("err = %q, want the elision marked", err)
	}
}

// ------------------------------------------------- request plumbing

// TestPostReportsAnUnencodablePayload covers the first step of a request. A
// silent failure here would look like an empty response.
func TestPostReportsAnUnencodablePayload(t *testing.T) {
	client := &httpClient{info: Provider{Label: "OpenAI"}, http: &http.Client{}}
	_, err := client.postWithStall(context.Background(), "http://127.0.0.1:1", nil, make(chan int), time.Second)
	if err == nil {
		t.Fatalf("a payload that cannot be encoded must be reported")
	}
	if !strings.Contains(err.Error(), "encode") {
		t.Errorf("err = %v, want it to name the encode step", err)
	}
}

// TestPostStopsOnACancelledContext keeps Esc responsive: a cancelled run must
// not be retried.
func TestPostStopsOnACancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The concrete client is used directly because the retry helper is not part
	// of the Client interface.
	client := &httpClient{
		info:    Provider{ID: "openai-compatible", Label: "Compatible", Kind: KindOpenAI},
		baseURL: server.URL,
		http:    &http.Client{},
	}

	started := time.Now()
	if _, err := client.postWithStall(ctx, server.URL+"/chat/completions", nil, map[string]any{"a": 1}, time.Second); err == nil {
		t.Fatalf("a cancelled request must fail")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the cancelled request took %s; it must not back off first", elapsed)
	}
}

// TestPostRejectsAnUnusableURL keeps a bad base URL from being retried three
// times while the operator waits.
func TestPostRejectsAnUnusableURL(t *testing.T) {
	client := &httpClient{info: Provider{Label: "OpenAI"}, http: &http.Client{}}
	started := time.Now()
	_, err := client.postWithStall(context.Background(), "http://\x7f invalid", nil, map[string]any{"a": 1}, time.Second)
	if err == nil {
		t.Fatalf("an unusable URL must be reported")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("a malformed URL took %s; it must not be retried", elapsed)
	}
}

// TestParseRetryAfterCoversBothSpellings pins the header parser, which decides
// how long a rate-limited run waits.
func TestParseRetryAfterCoversBothSpellings(t *testing.T) {
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("an absent header = %s, want 0", got)
	}
	if got := parseRetryAfter("3"); got != 3*time.Second {
		t.Errorf("seconds = %s, want 3s", got)
	}
	// A negative delay is nonsense; waiting zero is the safe reading.
	if got := parseRetryAfter("-5"); got != 0 {
		t.Errorf("a negative value = %s, want 0", got)
	}
	// A date in the past means "retry now", not a negative wait.
	if got := parseRetryAfter(time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)); got != 0 {
		t.Errorf("a past date = %s, want 0", got)
	}
	if got := parseRetryAfter(time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)); got < 20*time.Second {
		t.Errorf("a future date = %s, want roughly 30s", got)
	}
	// Anything unparseable is treated as no hint.
	if got := parseRetryAfter("soon"); got != 0 {
		t.Errorf("unparseable = %s, want 0", got)
	}
}

func TestSleepContextWithoutADelayReturnsTheContextError(t *testing.T) {
	if err := sleepContext(context.Background(), 0); err != nil {
		t.Errorf("a zero delay with a live context = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the cancellation", err)
	}
}

// ------------------------------------------------- registry helpers

func TestModelByIDReportsAnUnknownModel(t *testing.T) {
	if _, ok := ModelByID("definitely-not-a-model"); ok {
		t.Errorf("an unknown id must report false")
	}
	known, ok := ModelByID("gpt-5.4-mini")
	if !ok || known.Provider != "openai" {
		t.Errorf("known = %+v, ok = %v", known, ok)
	}
}

func TestFindModelHandlesBlankAndUnknownQueries(t *testing.T) {
	if _, ok := FindModel("   "); ok {
		t.Errorf("a blank query must not match anything")
	}
	if _, ok := FindModel("zzz-nothing-like-this"); ok {
		t.Errorf("an unknown query must not match")
	}
	// An exact id, a label and a substring all resolve, which is what makes
	// /model forgiving about what the operator types.
	for _, query := range []string{"gpt-5.4-mini", "GPT-5.4 mini", "5.4-mini"} {
		model, ok := FindModel(query)
		if !ok {
			t.Errorf("FindModel(%q) found nothing", query)
			continue
		}
		if model.ID != "gpt-5.4-mini" {
			t.Errorf("FindModel(%q) = %q", query, model.ID)
		}
	}
}

// TestModelFromQueryIsTheSameRouteForEveryCaller is the consistency rule: the
// command line and the in-app /model must record a selection identically,
// because the stored id is what doctor, the status bar and a price override all
// key on.
func TestModelFromQueryIsTheSameRouteForEveryCaller(t *testing.T) {
	cases := []struct {
		query        string
		wantID       string
		wantProvider string
		wantOK       bool
	}{
		{"gpt-5.4-mini", "gpt-5.4-mini", "openai", true},
		{"GPT-5.4 mini", "gpt-5.4-mini", "openai", true},
		{"openai:gpt-9-experimental", "gpt-9-experimental", "openai", true},
		// A local model tag keeps its colon: it belongs to the tag, not to a
		// provider prefix.
		{"qwen2.5-coder:latest", "qwen2.5-coder:latest", "ollama", true},
		{"not-a-provider:thing", "", "", false},
		{"   ", "", "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.query, func(t *testing.T) {
			model, ok := ModelFromQuery(testCase.query)
			if ok != testCase.wantOK {
				t.Fatalf("ok = %v, want %v", ok, testCase.wantOK)
			}
			if !ok {
				return
			}
			if model.ID != testCase.wantID {
				t.Errorf("ID = %q, want %q", model.ID, testCase.wantID)
			}
			if model.Provider != testCase.wantProvider {
				t.Errorf("Provider = %q, want %q", model.Provider, testCase.wantProvider)
			}
		})
	}
}

// TestPricedByIDSeparatesFreePricedAndUnknown is what lets a diagnostic say
// whether a cost cap can fire before a single token has been spent.
func TestPricedByIDSeparatesFreePricedAndUnknown(t *testing.T) {
	cfg := config.Default()

	// A catalogued model has a price from the table.
	if _, known := PricedByID(cfg, "gpt-5.4-mini"); !known {
		t.Errorf("a catalogued model should be priced")
	}
	// A local model is known-free rather than unpriced.
	if price, known := PricedByID(cfg, "qwen2.5-coder:latest"); !known || price.Known() {
		t.Errorf("a local model = %+v (known %v), want known and zero", price, known)
	}
	// Nobody has priced this one, so a cap over it cannot work.
	if _, known := PricedByID(cfg, "vendor:brand-new-model"); known {
		t.Errorf("an unpriced model must report itself as unknown")
	}
	if _, known := PricedByID(cfg, "   "); known {
		t.Errorf("a blank id cannot be priced")
	}

	// An override makes it budgetable, keyed either the way the app stores it
	// or the way a hand-edited config spells it.
	for _, id := range []string{"brand-new-model", "vendor:brand-new-model"} {
		withOverride := config.Default()
		withOverride.ModelPricing = map[string]config.ModelPrice{
			"brand-new-model": {InputPerMillion: 3, OutputPerMillion: 9},
		}
		price, known := PricedByID(withOverride, id)
		if !known || price.InputPerMillion != 3 {
			t.Errorf("PricedByID(%q) = %+v (known %v), want the override to apply", id, price, known)
		}
	}
}

func TestOverridesFromSkipsTheEmptyMap(t *testing.T) {
	// A nil result keeps the common case, no overrides at all, free of an
	// allocation on every model change.
	if got := OverridesFrom(nil); got != nil {
		t.Errorf("OverridesFrom(nil) = %v, want nil", got)
	}
	if got := OverridesFrom(map[string]config.ModelPrice{}); got != nil {
		t.Errorf("OverridesFrom(empty) = %v, want nil", got)
	}
	got := OverridesFrom(map[string]config.ModelPrice{"m": {InputPerMillion: 1, OutputPerMillion: 2}})
	if got["m"].InputPerMillion != 1 || got["m"].OutputPerMillion != 2 {
		t.Errorf("OverridesFrom = %+v", got)
	}
}

func TestDefaultBaseURLFallsBackForAnUnknownProvider(t *testing.T) {
	if got := DefaultBaseURL("not-a-provider"); got != "" {
		t.Errorf("DefaultBaseURL = %q, want empty", got)
	}
	if got := DefaultBaseURL("openai"); !strings.HasPrefix(got, "https://") {
		t.Errorf("DefaultBaseURL = %q", got)
	}
}

// TestPricingWithIgnoresAnAllZeroOverride keeps a placeholder entry from
// silently making a priced model look free. The production default is to fall
// back to the table, which is what makes a budget usable.
func TestPricingWithIgnoresAnAllZeroOverride(t *testing.T) {
	model := Model{ID: "local-alias", Provider: "openai", APIID: "gpt-5.4-mini"}
	blanks := map[string]Pricing{"gpt-5.4-mini": {}, "local-alias": {}}
	got := model.PricingWith(blanks)
	if !got.Known() {
		t.Errorf("an all-zero override should fall back to the table, got %+v", got)
	}
	if got.InputPerMillion == 0 {
		t.Errorf("price = %+v, want the catalogued rate", got)
	}
}

// ------------------------------------------------- encoders

func TestEncodeOpenAIToolsDefaultsAMissingSchema(t *testing.T) {
	encoded := encodeOpenAITools([]ToolDef{{Name: "bare", Description: "no schema"}})
	if len(encoded) != 1 {
		t.Fatalf("encoded = %#v", encoded)
	}
	function, ok := encoded[0]["function"].(map[string]any)
	if !ok {
		t.Fatalf("function = %#v", encoded[0]["function"])
	}
	parameters, ok := function["parameters"].(map[string]any)
	if !ok || parameters["type"] != "object" {
		t.Errorf("parameters = %#v, want an empty object schema", function["parameters"])
	}
}

func TestDecodeArgumentsHandlesBlankAndMalformedInput(t *testing.T) {
	if got, err := decodeArguments("   "); err != nil || len(got) != 0 {
		t.Errorf("a blank payload = %v, %v; want an empty object", got, err)
	}
	if got, err := decodeArguments("null"); err != nil || len(got) != 0 {
		t.Errorf("a null payload = %v, %v; want an empty object", got, err)
	}
	if _, err := decodeArguments("{"); err == nil {
		t.Errorf("a truncated payload must be rejected")
	}
	if _, err := decodeArguments("[1,2]"); err == nil {
		t.Errorf("an array is not an argument object")
	}
	decoded, err := decodeArguments(`{"path":"a.go"}`)
	if err != nil || decoded["path"] != "a.go" {
		t.Errorf("decoded = %v, err = %v", decoded, err)
	}
}

func TestAnthropicToolResultPlacesFalseForAnEmptyResult(t *testing.T) {
	// An empty tool result is normal (a command with no output) but the API
	// wants content, so a placeholder is sent rather than an empty string.
	blank := anthropicToolResult(Message{ToolID: "t1"})
	if blank["content"] != "(no output)" {
		t.Errorf("content = %#v, want the placeholder", blank["content"])
	}
	if blank["tool_use_id"] != "t1" {
		t.Errorf("tool_use_id = %#v", blank["tool_use_id"])
	}
	real := anthropicToolResult(Message{ToolID: "t2", Content: "the output"})
	if real["content"] != "the output" {
		t.Errorf("content = %#v, want the real output", real["content"])
	}
}

// TestEncodeAnthropicMessagesEmptiesAnAssistantTurnWithNoBlocks keeps a turn
// that was only a tool call, with no text, from producing an empty content
// array, which the API rejects.
func TestEncodeAnthropicMessagesEmptiesAnAssistantTurnWithNoBlocks(t *testing.T) {
	encoded := encodeAnthropicMessages([]Message{{Role: RoleAssistant}})
	if len(encoded) != 1 {
		t.Fatalf("encoded = %#v", encoded)
	}
	blocks, ok := encoded[0]["content"].([]map[string]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("content = %#v, want one placeholder block", encoded[0]["content"])
	}
	if blocks[0]["type"] != "text" {
		t.Errorf("block = %#v", blocks[0])
	}
}

func TestNewClientBuildsTheRightKindForEachProvider(t *testing.T) {
	cases := []struct {
		id   string
		want any
	}{
		{"anthropic", &anthropicClient{}},
		{"google", &googleClient{}},
		{"openai-compatible", &openAIClient{}},
	}
	for _, testCase := range cases {
		// A key is supplied for every provider so the kind, not the key check,
		// is what this asserts.
		client, err := NewClient(testCase.id, "https://example.test", func(string) string { return "test-key" })
		if err != nil {
			t.Fatalf("NewClient(%s): %v", testCase.id, err)
		}
		if fmt.Sprintf("%T", client) != fmt.Sprintf("%T", testCase.want) {
			t.Errorf("NewClient(%s) = %T, want %T", testCase.id, client, testCase.want)
		}
		if got := client.ID(); got != testCase.id {
			t.Errorf("ID() = %q, want %q", got, testCase.id)
		}
	}
}
