package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/99apps-id/termixgo/internal/channel"
)

type scriptedStreamAgent struct {
	scriptedAgent
	streamFn func(ctx context.Context, req channel.Request, prompt string, onEvent func(StreamEvent)) (string, error)
}

func (a *scriptedStreamAgent) RunPromptStream(ctx context.Context, req channel.Request, prompt string, onEvent func(StreamEvent)) (string, error) {
	a.mu.Lock()
	a.prompts = append(a.prompts, prompt)
	a.mu.Unlock()
	return a.streamFn(ctx, req, prompt, onEvent)
}

type stopChatMockAgent struct {
	scriptedAgent
	stopChatResult bool
	stopChatCalls  int
}

func (a *stopChatMockAgent) StopChat(req channel.Request) bool {
	a.stopChatCalls++
	return a.stopChatResult
}

func TestStreamLiveTextDeltasAndFinalization(t *testing.T) {
	prev := streamEditInterval
	streamEditInterval = 15 * time.Millisecond
	t.Cleanup(func() { streamEditInterval = prev })

	api := newRecordingAPI(t)
	agent := &scriptedStreamAgent{
		scriptedAgent: scriptedAgent{model: "test-model"},
		streamFn: func(ctx context.Context, req channel.Request, prompt string, onEvent func(StreamEvent)) (string, error) {
			onEvent(StreamEvent{Kind: "turn-start"})
			onEvent(StreamEvent{Kind: "thinking", Text: "reasoning..."})
			time.Sleep(25 * time.Millisecond)
			onEvent(StreamEvent{Kind: "tool-start", ToolName: "read_file", Text: "Reading main.go"})
			time.Sleep(25 * time.Millisecond)
			onEvent(StreamEvent{Kind: "tool-end", ToolName: "read_file", Text: "Read main.go", ToolOK: true})
			time.Sleep(25 * time.Millisecond)
			onEvent(StreamEvent{Kind: "text", Text: "Hello "})
			onEvent(StreamEvent{Kind: "text", Text: "**world**!\n\n```go\nfmt.Println(1)"})
			time.Sleep(35 * time.Millisecond)
			onEvent(StreamEvent{Kind: "text", Text: "\n```"})
			return "Hello **world**!\n\n```go\nfmt.Println(1)\n```", nil
		},
	}

	bot, _ := pairedBot(t, agent)
	bot.client = api.client()

	bot.handleMessage(context.Background(), message("explain main.go"))

	sent := api.calls("sendMessage")
	if len(sent) != 1 {
		t.Fatalf("expected 1 sendMessage (initial card), got %d", len(sent))
	}
	edits := api.calls("editMessageText")
	if len(edits) < 2 {
		t.Fatalf("expected multiple live edits, got %d", len(edits))
	}

	sawCursor := false
	sawClosedMidStreamCode := false
	for i := 0; i < len(edits)-1; i++ {
		txt, _ := edits[i]["text"].(string)
		if strings.Contains(txt, streamCursor) {
			sawCursor = true
		}
		if strings.Contains(txt, "<pre><code class=\"language-go\">") && strings.Contains(txt, "</code></pre>") {
			sawClosedMidStreamCode = true
		}
	}
	if !sawCursor {
		t.Errorf("expected live streaming cursor %q in intermediate edits", streamCursor)
	}
	if !sawClosedMidStreamCode {
		t.Errorf("expected mid-stream unclosed code fence to render safely closed in HTML")
	}

	final, _ := edits[len(edits)-1]["text"].(string)
	if strings.Contains(final, streamCursor) {
		t.Errorf("final message must not contain live cursor, got %q", final)
	}
	if !strings.Contains(final, "<b>world</b>") || !strings.Contains(final, "<pre><code class=\"language-go\">") {
		t.Errorf("final message missing expected HTML formatting: %q", final)
	}
}

func TestStreamMultiMessageOverflow(t *testing.T) {
	prev := streamEditInterval
	streamEditInterval = 10 * time.Millisecond
	t.Cleanup(func() { streamEditInterval = prev })

	api := newRecordingAPI(t)
	line := "fmt.Println(\"streaming line inside a long code block\")\n"
	part1 := "Intro:\n\n```go\n" + strings.Repeat(line, 75) // > 3800 bytes
	part2 := strings.Repeat(line, 10) + "```\nDone."
	fullAnswer := part1 + part2

	agent := &scriptedStreamAgent{
		scriptedAgent: scriptedAgent{model: "test-model"},
		streamFn: func(ctx context.Context, req channel.Request, prompt string, onEvent func(StreamEvent)) (string, error) {
			onEvent(StreamEvent{Kind: "text", Text: part1})
			time.Sleep(40 * time.Millisecond)
			onEvent(StreamEvent{Kind: "text", Text: part2})
			return fullAnswer, nil
		},
	}

	bot, _ := pairedBot(t, agent)
	bot.client = api.client()
	bot.handleMessage(context.Background(), message("generate code"))

	sent := api.calls("sendMessage")
	if len(sent) < 2 {
		t.Fatalf("expected overflow during streaming to open a 2nd message, got %d sendMessage call(s)", len(sent))
	}
	for i, call := range sent {
		txt, _ := call["text"].(string)
		if len(txt) > messageLimit {
			t.Errorf("sendMessage[%d] length %d exceeds Telegram cap %d", i, len(txt), messageLimit)
		}
	}
	edits := api.calls("editMessageText")
	last, _ := edits[len(edits)-1]["text"].(string)
	if strings.Contains(last, streamCursor) {
		t.Errorf("final message must not retain cursor: %q", last)
	}
	if !strings.Contains(last, "Done.") {
		t.Errorf("final continuation message missing tail content: %q", last)
	}
}

func TestStreamToolResetClearsPreamble(t *testing.T) {
	prev := streamEditInterval
	streamEditInterval = 10 * time.Millisecond
	t.Cleanup(func() { streamEditInterval = prev })

	api := newRecordingAPI(t)
	agent := &scriptedStreamAgent{
		scriptedAgent: scriptedAgent{model: "test-model"},
		streamFn: func(ctx context.Context, req channel.Request, prompt string, onEvent func(StreamEvent)) (string, error) {
			onEvent(StreamEvent{Kind: "text", Text: "Let me check that first."})
			time.Sleep(25 * time.Millisecond)
			onEvent(StreamEvent{Kind: "tool-start", ToolName: "read_file", Text: "Reading foo.go"})
			time.Sleep(25 * time.Millisecond)
			onEvent(StreamEvent{Kind: "tool-end", ToolName: "read_file", Text: "Read foo.go", ToolOK: true})
			onEvent(StreamEvent{Kind: "text", Text: "Final answer."})
			return "Final answer.", nil
		},
	}

	bot, _ := pairedBot(t, agent)
	bot.client = api.client()
	bot.handleMessage(context.Background(), message("check foo"))

	edits := api.calls("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("expected edits")
	}
	final, _ := edits[len(edits)-1]["text"].(string)
	if final != "Final answer." {
		t.Errorf("final = %q, want %q", final, "Final answer.")
	}
}

func TestStreamDeletesSilentCard(t *testing.T) {
	api := newRecordingAPI(t)
	agent := &scriptedStreamAgent{
		scriptedAgent: scriptedAgent{model: "test-model"},
		streamFn: func(ctx context.Context, req channel.Request, prompt string, onEvent func(StreamEvent)) (string, error) {
			return "[SILENT]", nil
		},
	}

	bot, _ := pairedBot(t, agent)
	bot.client = api.client()
	bot.handleMessage(context.Background(), message("quietly"))

	if !api.called("deleteMessage") {
		t.Errorf("expected silent response to delete progress card")
	}
}

func TestTelegramAuditBugFixes(t *testing.T) {
	// 1. openedFence tracks the latest open fence language across multiple fences.
	multiFence := "```go\nfmt.Println(1)\n```\n\n```python\nprint(1)"
	if lang, open := openedFence(multiFence); !open || lang != "python" {
		t.Errorf("openedFence(multiFence) = (%q, %v), want (\"python\", true)", lang, open)
	}

	// 2. entitiesMentionBot checks exact @username boundary and bot_command mentions.
	bot := New("123:abc", nil)
	bot.setIdentity(User{ID: 99, Username: "termixgo_bot"})
	if bot.mentionsBot(&Message{
		Text:     "@termixgo_bot_dev hello",
		Entities: []MessageEntity{{Type: "mention", Offset: 0, Length: 17}},
	}) {
		t.Errorf("@termixgo_bot_dev must not match @termixgo_bot")
	}
	if !bot.mentionsBot(&Message{
		Text:     "/status@termixgo_bot",
		Entities: []MessageEntity{{Type: "bot_command", Offset: 0, Length: 20}},
	}) {
		t.Errorf("/status@termixgo_bot must count as mentioning the bot")
	}

	// 3. Relative markdown links do not emit invalid Telegram HTML <a> tags.
	relHTML := markdownToTelegramHTML("see [main.go](main.go) and [docs](https://example.com)")
	if strings.Contains(relHTML, `href="main.go"`) {
		t.Errorf("relative link must not produce <a href=\"main.go\">: %q", relHTML)
	}
	if !strings.Contains(relHTML, `<a href="https://example.com">docs</a>`) {
		t.Errorf("https link must remain an anchor tag: %q", relHTML)
	}

	// 4. clampCaption respects Telegram's 1024-byte caption cap.
	longCaption := strings.Repeat("a", 1500)
	if got := clampCaption(longCaption); len(got) > captionLimit {
		t.Errorf("clampCaption length = %d, want <= %d", len(got), captionLimit)
	}

	// 5. /stop with StopChat returning false must not fall back to global Stop().
	stopMock := &stopChatMockAgent{stopChatResult: false}
	api := newRecordingAPI(t)
	stopBot := New("123:abc", stopMock)
	stopBot.client = api.client()
	stopBot.Pair(7, 9)
	stopBot.handleMessage(context.Background(), message("/stop"))
	if stopMock.stopChatCalls != 1 {
		t.Errorf("StopChat calls = %d, want 1", stopMock.stopChatCalls)
	}
	if stopMock.stopCallsCount() != 0 {
		t.Errorf("global Stop() must not be called when StopChat is implemented and returns false")
	}
	if !strings.Contains(lastText(t, api, "sendMessage"), "No turn is currently running") {
		t.Errorf("expected idle chat stop message, got %q", lastText(t, api, "sendMessage"))
	}
}

func TestRunVoiceSendsTranscribedConfirmationAsHTML(t *testing.T) {
	audioBytes := []byte{0x4f, 0x67, 0x67, 0x53}
	var mu sync.Mutex
	var sentPayloads []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/getFile"):
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"file_id": "v1", "file_path": "voice/note.oga"},
			})
		case strings.Contains(request.URL.Path, "/file/"):
			_, _ = writer.Write(audioBytes)
		case strings.HasSuffix(request.URL.Path, "/sendMessage"):
			raw, _ := io.ReadAll(request.Body)
			var payload map[string]any
			_ = json.Unmarshal(raw, &payload)
			mu.Lock()
			sentPayloads = append(sentPayloads, payload)
			mu.Unlock()
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
		default:
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
		}
	}))
	defer server.Close()

	agent := &scriptedAgent{answer: "ok", model: "test-model", transcribeText: "check <main>"}
	bot := New("123:abc", agent)
	bot.client = newClientAt("123:abc", server.URL)
	bot.Pair(7, 9)

	bot.handleMessage(context.Background(), &Message{
		Chat:  Chat{ID: 7, Type: "private"},
		From:  &User{ID: 9},
		Voice: &Voice{FileID: "v1", Duration: 2, MimeType: "audio/ogg"},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(sentPayloads) == 0 {
		t.Fatalf("expected sendMessage calls")
	}
	first := sentPayloads[0]
	if first["parse_mode"] != "HTML" {
		t.Errorf("transcribed confirmation must use parse_mode=HTML, got %v", first["parse_mode"])
	}
	text, _ := first["text"].(string)
	if !strings.Contains(text, "<i>\"check &lt;main&gt;\"</i>") {
		t.Errorf("transcribed confirmation text = %q", text)
	}
}

func TestEnrichedMarkdownTelegram(t *testing.T) {
	input := "## Ringkasan 📋\n\n*Catatan penting* dan __tebal__ serta ~~coret~~.\n- Poin *pertama*\n1. Langkah **kedua**\n---"
	got := markdownToTelegramHTML(input)
	for _, want := range []string{
		"<b>Ringkasan 📋</b>",
		"<i>Catatan penting</i>",
		"<b>tebal</b>",
		"<s>coret</s>",
		"• Poin <i>pertama</i>",
		"1. Langkah <b>kedua</b>",
		"──────────",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("enriched Telegram HTML missing %q:\ngot:\n%s", want, got)
		}
	}
	// Multiplication with spaces must not become italics.
	if math := markdownToTelegramHTML("2 * 3 * 4"); math != "2 * 3 * 4" {
		t.Errorf("math expression got mangled: %q", math)
	}
}
