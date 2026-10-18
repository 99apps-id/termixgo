package telegram

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMarkdownToTelegramHTML(t *testing.T) {
	cases := map[string]string{
		"plain text":                          "plain text",
		"**bold**":                            "<b>bold</b>",
		"`code`":                              "<code>code</code>",
		"# Heading":                           "<b>Heading</b>",
		"[x](https://e.com)":                  `<a href="https://e.com">x</a>`,
		"a < b & c > d":                       "a &lt; b &amp; c &gt; d",
		"see `a<b` here":                      "see <code>a&lt;b</code> here",
		"| a | b |\n| --- | --- |\n| 1 | 2 |": "<pre>a | b\n| --- | --- |\n1 | 2\n</pre>",
		"| a & b | c < d |":                   "<pre>a &amp; b | c &lt; d\n</pre>",
	}
	for input, want := range cases {
		if got := markdownToTelegramHTML(input); got != want {
			t.Errorf("markdownToTelegramHTML(%q) = %q, want %q", input, got, want)
		}
	}

	code := markdownToTelegramHTML("```\nif a < b {}\n```")
	if !strings.Contains(code, "<pre>") || !strings.Contains(code, "a &lt; b") || !strings.Contains(code, "</pre>") {
		t.Errorf("fenced code = %q", code)
	}

	linenos := markdownToTelegramHTML("```python linenos\nx = 1\n```")
	if !strings.Contains(linenos, "<pre>") || !strings.Contains(linenos, "1: x = 1") || !strings.Contains(linenos, "</pre>") {
		t.Errorf("linenos code = %q", linenos)
	}
}

func TestSendMarkdownUsesHTML(t *testing.T) {
	api := newRecordingAPI(t)
	client := api.client()
	if _, err := client.SendMarkdown(context.Background(), 7, "**bold** and `x`", nil); err != nil {
		t.Fatalf("SendMarkdown: %v", err)
	}
	calls := api.calls("sendMessage")
	last := calls[len(calls)-1]
	if last["parse_mode"] != "HTML" {
		t.Errorf("parse_mode = %v, want HTML", last["parse_mode"])
	}
	if last["text"] != "<b>bold</b> and <code>x</code>" {
		t.Errorf("text = %v", last["text"])
	}
}

// TestSendMarkdownFallsBackWhenTelegramRejectsTheEntities keeps a formatting bug
// from swallowing an answer: a rejected entity body is resent as plain text.
func TestSendMarkdownFallsBackWhenTelegramRejectsTheEntities(t *testing.T) {
	var withParse, without int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if _, ok := payload["parse_mode"]; ok {
			withParse++
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"ok": false, "error_code": 400, "description": "Bad Request: can't parse entities",
			})
			return
		}
		without++
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	}))
	defer server.Close()

	client := newClientAt("123:abc", server.URL)
	if _, err := client.SendMarkdown(context.Background(), 7, "**hi**", nil); err != nil {
		t.Fatalf("SendMarkdown: %v", err)
	}
	if withParse != 1 || without != 1 {
		t.Errorf("withParse=%d without=%d, want one of each", withParse, without)
	}
}

// TestLongAnswerIsSplitIntoWholeMessages keeps a long answer from being
// truncated at the Telegram cap: the whole answer must arrive, split over
// several messages.
func TestLongAnswerIsSplitIntoWholeMessages(t *testing.T) {
	api := newRecordingAPI(t)
	bot := New("123:abc", &scriptedAgent{})
	bot.client = api.client()

	var builder strings.Builder
	for index := 0; index < 2000; index++ {
		fmt.Fprintf(&builder, "line %d of a long answer\n", index)
	}
	answer := builder.String()

	bot.replyMarkdown(context.Background(), 7, answer)

	sent := api.calls("sendMessage")
	if len(sent) < 2 {
		t.Fatalf("a long answer should be split, got %d message(s)", len(sent))
	}
	var joined strings.Builder
	for _, payload := range sent {
		text, _ := payload["text"].(string)
		if len(text) > 4096 {
			t.Errorf("a chunk is %d bytes, over the Telegram limit", len(text))
		}
		joined.WriteString(text)
	}
	// The renderer trims trailing newlines, so the answer's final one is not
	// expected back; every other byte must survive.
	if got := strings.TrimRight(joined.String(), "\n"); got != strings.TrimRight(answer, "\n") {
		t.Errorf("the chunks do not rebuild the answer: got %d bytes, want %d", len(got), len(strings.TrimRight(answer, "\n")))
	}
}

// TestPhotoMessageRunsWithTheImage covers an incoming photo: the largest size is
// downloaded and handed to the agent as a vision attachment, with the caption as
// the prompt.
func TestPhotoMessageRunsWithTheImage(t *testing.T) {
	image := []byte{0xff, 0xd8, 0xff, 0x00, 0x01}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/getFile"):
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"file_id": "big", "file_path": "photos/file_1.jpg"},
			})
		case strings.Contains(request.URL.Path, "/file/"):
			_, _ = writer.Write(image)
		default:
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
		}
	}))
	defer server.Close()

	agent := &scriptedAgent{answer: "a cat", model: "test-model"}
	bot := New("123:abc", agent)
	bot.client = newClientAt("123:abc", server.URL)
	bot.Pair(7, 9)

	bot.handleMessage(context.Background(), &Message{
		Chat:    Chat{ID: 7, Type: "private"},
		From:    &User{ID: 9},
		Caption: "what is this?",
		Photo:   []PhotoSize{{FileID: "small", Width: 90}, {FileID: "big", Width: 900}},
	})

	if got := agent.firstPrompt(); got != "what is this?" {
		t.Errorf("prompt = %q", got)
	}
	mediaType, data := agent.imageCall()
	if mediaType != "image/jpeg" {
		t.Errorf("media type = %q, want image/jpeg", mediaType)
	}
	if data != base64.StdEncoding.EncodeToString(image) {
		t.Errorf("image data was not the downloaded bytes")
	}
}

func TestDocumentMessageRunsWithTextDocument(t *testing.T) {
	docContent := []byte("func main() { println(\"hello world\") }")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/getFile"):
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"file_id": "doc123", "file_path": "documents/main.go"},
			})
		case strings.Contains(request.URL.Path, "/file/"):
			_, _ = writer.Write(docContent)
		default:
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
		}
	}))
	defer server.Close()

	tempDir := t.TempDir()
	agent := &scriptedAgent{answer: "file inspected", model: "test-model", workspace: tempDir}
	bot := New("123:abc", agent)
	bot.client = newClientAt("123:abc", server.URL)
	bot.Pair(7, 9)

	bot.handleMessage(context.Background(), &Message{
		Chat:    Chat{ID: 7, Type: "private"},
		From:    &User{ID: 9},
		Caption: "Please refactor this",
		Document: &Document{
			FileID:   "doc123",
			FileName: "main.go",
			MimeType: "text/x-go",
		},
	})

	prompt := agent.firstPrompt()
	if !strings.Contains(prompt, "main.go") {
		t.Errorf("prompt should mention filename: %s", prompt)
	}
	if !strings.Contains(prompt, "func main()") {
		t.Errorf("prompt should contain file content: %s", prompt)
	}
	if !strings.Contains(prompt, "Please refactor this") {
		t.Errorf("prompt should contain caption: %s", prompt)
	}
}

func TestDocumentMessageRunsWithImageDocument(t *testing.T) {
	image := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/getFile"):
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"file_id": "imgdoc", "file_path": "documents/photo.png"},
			})
		case strings.Contains(request.URL.Path, "/file/"):
			_, _ = writer.Write(image)
		default:
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
		}
	}))
	defer server.Close()

	agent := &scriptedAgent{answer: "screenshot seen", model: "test-model"}
	bot := New("123:abc", agent)
	bot.client = newClientAt("123:abc", server.URL)
	bot.Pair(7, 9)

	bot.handleMessage(context.Background(), &Message{
		Chat:    Chat{ID: 7, Type: "private"},
		From:    &User{ID: 9},
		Caption: "look at screenshot",
		Document: &Document{
			FileID:   "imgdoc",
			FileName: "screenshot.png",
			MimeType: "image/png",
		},
	})

	if got := agent.firstPrompt(); got != "look at screenshot" {
		t.Errorf("prompt = %q", got)
	}
	mediaType, data := agent.imageCall()
	if mediaType != "image/png" {
		t.Errorf("media type = %q, want image/png", mediaType)
	}
	if data != base64.StdEncoding.EncodeToString(image) {
		t.Errorf("image data was not the downloaded bytes")
	}
}

func TestVoiceMessageTranscribesAndRunsPrompt(t *testing.T) {
	audioBytes := []byte{0x4f, 0x67, 0x67, 0x53} // OggS
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/getFile"):
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"file_id": "voice1", "file_path": "voice/note.oga"},
			})
		case strings.Contains(request.URL.Path, "/file/"):
			_, _ = writer.Write(audioBytes)
		default:
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
		}
	}))
	defer server.Close()

	agent := &scriptedAgent{
		answer:         "tests fixed",
		model:          "test-model",
		transcribeText: "run all tests and fix failing ones",
	}
	bot := New("123:abc", agent)
	bot.client = newClientAt("123:abc", server.URL)
	bot.Pair(7, 9)

	bot.handleMessage(context.Background(), &Message{
		Chat: Chat{ID: 7, Type: "private"},
		From: &User{ID: 9},
		Voice: &Voice{
			FileID:   "voice1",
			Duration: 3,
			MimeType: "audio/ogg",
		},
	})

	if got := agent.firstPrompt(); got != "run all tests and fix failing ones" {
		t.Errorf("prompt = %q, want transcribed text", got)
	}
}
