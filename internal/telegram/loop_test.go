package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ------------------------------------------------------------------ API client

// recordingAPI is a fake Bot API that captures the payload of every call, so a
// test can assert on what would have gone over the wire.
type recordingAPI struct {
	mu       sync.Mutex
	methods  []string
	payloads map[string][]map[string]any
	server   *httptest.Server
	respond  func(method string, payload map[string]any) (any, bool)
}

func newRecordingAPI(t *testing.T) *recordingAPI {
	t.Helper()
	api := &recordingAPI{payloads: map[string][]map[string]any{}}
	api.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]

		api.mu.Lock()
		api.methods = append(api.methods, method)
		api.payloads[method] = append(api.payloads[method], payload)
		handler := api.respond
		api.mu.Unlock()

		if handler != nil {
			if result, ok := handler(method, payload); ok {
				_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": result})
				return
			}
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	}))
	t.Cleanup(api.server.Close)
	return api
}

func (api *recordingAPI) client() *Client { return newClientAt("123:abc", api.server.URL) }

func (api *recordingAPI) calls(method string) []map[string]any {
	api.mu.Lock()
	defer api.mu.Unlock()
	return append([]map[string]any(nil), api.payloads[method]...)
}

func (api *recordingAPI) called(method string) bool {
	api.mu.Lock()
	defer api.mu.Unlock()
	for _, name := range api.methods {
		if name == method {
			return true
		}
	}
	return false
}

func TestClientGetMeReturnsTheBotIdentity(t *testing.T) {
	api := newRecordingAPI(t)
	api.respond = func(method string, payload map[string]any) (any, bool) {
		if method != "getMe" {
			return nil, false
		}
		return map[string]any{"id": 42, "username": "termixgo_bot"}, true
	}

	user, err := api.client().GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if user.ID != 42 || user.Username != "termixgo_bot" {
		t.Errorf("user = %+v", user)
	}
}

func TestClientGetUpdatesSendsOnlyTheGivenOffset(t *testing.T) {
	api := newRecordingAPI(t)
	api.respond = func(method string, payload map[string]any) (any, bool) {
		return []any{}, true
	}
	client := api.client()

	if _, err := client.GetUpdates(context.Background(), 0, 0); err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	// A zero offset means "whatever is waiting"; sending it explicitly is
	// harmless but the absence is what the API expects.
	first := api.calls("getUpdates")[0]
	if _, present := first["offset"]; present {
		t.Errorf("a zero offset should not be sent, got %v", first)
	}
	if first["timeout"] != float64(0) {
		t.Errorf("timeout = %v, want 0", first["timeout"])
	}

	if _, err := client.GetUpdates(context.Background(), 7, 0); err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	second := api.calls("getUpdates")[1]
	if second["offset"] != float64(7) {
		t.Errorf("offset = %v, want 7", second["offset"])
	}

	// Only the two update kinds the bot understands should be requested,
	// otherwise Telegram queues edits and other noise for no reason.
	allowed, ok := first["allowed_updates"].([]any)
	if !ok || len(allowed) != 2 {
		t.Errorf("allowed_updates = %v, want message and callback_query", first["allowed_updates"])
	}
}

func TestClientGetUpdatesDecodesUpdates(t *testing.T) {
	api := newRecordingAPI(t)
	api.respond = func(method string, payload map[string]any) (any, bool) {
		if method != "getUpdates" {
			return nil, false
		}
		return []any{
			map[string]any{
				"update_id": 100,
				"message": map[string]any{
					"message_id": 5,
					"chat":       map[string]any{"id": 7, "type": "private"},
					"from":       map[string]any{"id": 9, "username": "operator"},
					"text":       "/status",
				},
			},
		}, true
	}

	updates, err := api.client().GetUpdates(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(updates))
	}
	update := updates[0]
	if update.UpdateID != 100 {
		t.Errorf("update id = %d", update.UpdateID)
	}
	if update.Message == nil || update.Message.Text != "/status" {
		t.Fatalf("message = %+v", update.Message)
	}
	if update.Message.Chat.ID != 7 || update.Message.From == nil || update.Message.From.ID != 9 {
		t.Errorf("chat or sender was not decoded: %+v", update.Message)
	}
}

func TestClientAnswerCallbackQueryIncludesTextOnlyWhenGiven(t *testing.T) {
	api := newRecordingAPI(t)
	client := api.client()

	if err := client.AnswerCallbackQuery(context.Background(), "cb-1", ""); err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}
	if _, present := api.calls("answerCallbackQuery")[0]["text"]; present {
		t.Errorf("an empty text should be omitted rather than sent as an empty string")
	}

	if err := client.AnswerCallbackQuery(context.Background(), "cb-2", "Not allowed."); err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}
	if got := api.calls("answerCallbackQuery")[1]["text"]; got != "Not allowed." {
		t.Errorf("text = %v", got)
	}
}

func TestClientSetMyCommandsPublishesTheMenu(t *testing.T) {
	api := newRecordingAPI(t)
	commands := []BotCommand{{Command: "run", Description: "Run a prompt"}}

	if err := api.client().SetMyCommands(context.Background(), commands); err != nil {
		t.Fatalf("SetMyCommands: %v", err)
	}
	calls := api.calls("setMyCommands")
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	entries, ok := calls[0]["commands"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("commands = %v", calls[0]["commands"])
	}
	entry := entries[0].(map[string]any)
	if entry["command"] != "run" {
		t.Errorf("entry = %v", entry)
	}
}

func TestClientEditMessageAndChatAction(t *testing.T) {
	api := newRecordingAPI(t)
	client := api.client()

	if err := client.EditMessageText(context.Background(), 7, 42, "updated"); err != nil {
		t.Fatalf("EditMessageText: %v", err)
	}
	edit := api.calls("editMessageText")[0]
	if edit["chat_id"] != float64(7) || edit["message_id"] != float64(42) || edit["text"] != "updated" {
		t.Errorf("payload = %v", edit)
	}

	if err := client.SendChatAction(context.Background(), 7, "typing"); err != nil {
		t.Fatalf("SendChatAction: %v", err)
	}
	if got := api.calls("sendChatAction")[0]["action"]; got != "typing" {
		t.Errorf("action = %v", got)
	}
}

func TestClientSendMessageIncludesAKeyboardOnlyWhenPresent(t *testing.T) {
	api := newRecordingAPI(t)
	client := api.client()

	if _, err := client.SendMessage(context.Background(), 7, "hello", nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if _, present := api.calls("sendMessage")[0]["reply_markup"]; present {
		t.Errorf("no keyboard was given, so none should be sent")
	}

	keyboard := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{{Text: "Approve", CallbackData: "ap:1"}}}}
	if _, err := client.SendMessage(context.Background(), 7, "hello", keyboard); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if _, present := api.calls("sendMessage")[1]["reply_markup"]; !present {
		t.Errorf("the keyboard should be sent when given")
	}

	// An empty keyboard is the same as none.
	empty := &InlineKeyboard{}
	if _, err := client.SendMessage(context.Background(), 7, "hello", empty); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if _, present := api.calls("sendMessage")[2]["reply_markup"]; present {
		t.Errorf("an empty keyboard should be omitted")
	}
}

// TestClientSendMessageClampsLongText is Telegram's hard limit. Exceeding it
// fails the whole send, so the text has to be trimmed first.
func TestClientSendMessageClampsLongText(t *testing.T) {
	api := newRecordingAPI(t)
	long := strings.Repeat("line of text\n", 400)

	if _, err := api.client().SendMessage(context.Background(), 7, long, nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	text, _ := api.calls("sendMessage")[0]["text"].(string)
	if len(text) > messageLimit {
		t.Errorf("text length = %d, want at most %d", len(text), messageLimit)
	}
	if !strings.Contains(text, "truncated") {
		t.Errorf("the truncation should be visible to the reader")
	}
}

// ------------------------------------------------------------------ API errors

func TestAPIErrorParsesTheRetryHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"ok":          false,
			"error_code":  429,
			"description": "Too Many Requests: retry after 12",
			"parameters":  map[string]any{"retry_after": 12},
		})
	}))
	defer server.Close()

	err := newClientAt("123:abc", server.URL).SendChatAction(context.Background(), 1, "typing")
	if err == nil {
		t.Fatalf("expected an error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Code != 429 {
		t.Errorf("code = %d", apiErr.Code)
	}
	if apiErr.RetryAfter != 12 {
		t.Errorf("retry after = %d, want 12", apiErr.RetryAfter)
	}
	if got := RetryAfterSeconds(err); got != 12 {
		t.Errorf("RetryAfterSeconds = %d, want 12", got)
	}
}

func TestAPIErrorFormatting(t *testing.T) {
	withMessage := &APIError{Code: 409, Description: "Conflict: terminated by other getUpdates"}
	formatted := withMessage.Error()
	if !strings.Contains(formatted, "409") || !strings.Contains(formatted, "Conflict") {
		t.Errorf("Error() = %q", formatted)
	}

	bare := &APIError{Code: 500}
	if got := bare.Error(); !strings.Contains(got, "500") {
		t.Errorf("Error() = %q, want the code", got)
	}
}

// TestClientReportsAnUnreadableReply keeps a proxy or a captive portal from
// looking like a successful empty response.
func TestClientReportsAnUnreadableReply(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte("<html>gateway error</html>"))
	}))
	defer server.Close()

	err := newClientAt("123:abc", server.URL).SendChatAction(context.Background(), 1, "typing")
	if err == nil {
		t.Fatalf("a non-JSON reply must not be treated as success")
	}
	if !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("error = %v, want it to say the reply was unreadable", err)
	}
}

func TestClientReportsATransportFailure(t *testing.T) {
	// A server that is closed before the call, standing in for no network.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	err := newClientAt("123:abc", url).SendChatAction(context.Background(), 1, "typing")
	if err == nil {
		t.Fatalf("an unreachable API must fail")
	}
}

// ------------------------------------------------------------------ poll loop

// TestRunAdvancesTheOffsetAndHandlesUpdates is the core of the bot: a missed
// offset advance means Telegram replays the same command forever.
func TestRunAdvancesTheOffsetAndHandlesUpdates(t *testing.T) {
	api := newRecordingAPI(t)
	agent := &recordingAgent{status: "status line"}

	var (
		mu      sync.Mutex
		polls   int
		offsets []any
	)
	cancel := func() {}
	api.respond = func(method string, payload map[string]any) (any, bool) {
		if method != "getUpdates" {
			return nil, false
		}
		mu.Lock()
		defer mu.Unlock()
		polls++
		offsets = append(offsets, payload["offset"])
		switch polls {
		case 1:
			return []any{map[string]any{
				"update_id": 10,
				"message": map[string]any{
					"chat": map[string]any{"id": 7, "type": "private"},
					"from": map[string]any{"id": 9},
					"text": "/status",
				},
			}}, true
		default:
			// Stop the loop once the update has been through once, so the
			// test does not spin against the fake.
			cancel()
			return []any{}, true
		}
	}

	bot := New("123:abc", agent)
	bot.client = api.client()
	bot.Pair(7, 9)

	ctx, stop := context.WithCancel(context.Background())
	cancel = stop
	defer stop()

	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		stop()
		t.Fatalf("the poll loop did not stop when the context was cancelled")
	}

	if !api.called("setMyCommands") {
		t.Errorf("the command menu should be published on start")
	}
	if !agent.called() {
		t.Errorf("the update was not handled")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(offsets) < 2 {
		t.Fatalf("expected at least two polls, got %d", len(offsets))
	}
	if offsets[0] != nil {
		t.Errorf("the first poll should carry no offset, got %v", offsets[0])
	}
	// The next poll must ask for everything after update 10, or Telegram
	// replays it and the command runs twice.
	if offsets[1] != float64(11) {
		t.Errorf("second poll offset = %v, want 11", offsets[1])
	}
}

// TestRunSkipsAReplayedUpdate covers the offset guard: Telegram may resend an
// update the bot has already seen, and running it again would repeat the
// command. This is the bug the guard exists for, and the dispatch used to
// happen outside it.
func TestRunSkipsAReplayedUpdate(t *testing.T) {
	api := newRecordingAPI(t)
	agent := &recordingAgent{status: "status line"}

	var mu sync.Mutex
	polls := 0
	cancel := func() {}
	api.respond = func(method string, payload map[string]any) (any, bool) {
		if method != "getUpdates" {
			return nil, false
		}
		mu.Lock()
		defer mu.Unlock()
		polls++
		if polls == 1 {
			// The loop starts at offset 5, so an update at id 4 is stale.
			return []any{map[string]any{
				"update_id": 4,
				"message": map[string]any{
					"chat": map[string]any{"id": 7, "type": "private"},
					"from": map[string]any{"id": 9},
					"text": "/status",
				},
			}}, true
		}
		cancel()
		return []any{}, true
	}

	bot := New("123:abc", agent)
	bot.client = api.client()
	bot.Pair(7, 9)
	bot.offset = 5

	ctx, stop := context.WithCancel(context.Background())
	cancel = stop
	defer stop()

	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		stop()
		t.Fatalf("the poll loop did not stop")
	}

	if agent.called() {
		t.Errorf("an update below the current offset must not be handled again")
	}
	if bot.offset != 5 {
		t.Errorf("offset = %d, want it unchanged at 5", bot.offset)
	}
}

// TestRunHandlesTheUpdateAtTheOffset pins the boundary. Telegram's offset is
// the first update to return, so the update sitting exactly on it is new and
// must be handled. Getting this off by one either skips a command or repeats
// one.
func TestRunHandlesTheUpdateAtTheOffset(t *testing.T) {
	api := newRecordingAPI(t)
	agent := &recordingAgent{status: "status line"}

	var mu sync.Mutex
	polls := 0
	cancel := func() {}
	api.respond = func(method string, payload map[string]any) (any, bool) {
		if method != "getUpdates" {
			return nil, false
		}
		mu.Lock()
		defer mu.Unlock()
		polls++
		if polls == 1 {
			return []any{map[string]any{
				"update_id": 5,
				"message": map[string]any{
					"chat": map[string]any{"id": 7, "type": "private"},
					"from": map[string]any{"id": 9},
					"text": "/status",
				},
			}}, true
		}
		cancel()
		return []any{}, true
	}

	bot := New("123:abc", agent)
	bot.client = api.client()
	bot.Pair(7, 9)
	bot.offset = 5

	ctx, stop := context.WithCancel(context.Background())
	cancel = stop
	defer stop()

	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		stop()
		t.Fatalf("the poll loop did not stop")
	}

	if !agent.called() {
		t.Errorf("the update at the current offset is new and must be handled")
	}
	if bot.offset != 6 {
		t.Errorf("offset = %d, want 6 after handling update 5", bot.offset)
	}
}

// TestRunRetriesAfterAPollFailure keeps a transient failure from ending the
// bot: a dropped connection should resume, not stop.
//
// The server is built here rather than reusing the recording fake, because a
// handler that fails the first call and succeeds afterwards is the point of
// the test and needs its own counter.
func TestRunRetriesAfterAPollFailure(t *testing.T) {
	var (
		mu    sync.Mutex
		polls int
	)
	polled := make(chan struct{})
	once := sync.Once{}
	cancel := make(chan struct{})
	stopOnce := sync.Once{}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		if method != "getUpdates" {
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{}})
			return
		}
		mu.Lock()
		polls++
		count := polls
		mu.Unlock()

		if count == 1 {
			// A gateway failure: the loop must back off and try again.
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = writer.Write([]byte("<html>down</html>"))
			once.Do(func() { close(polled) })
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": []any{}})
		stopOnce.Do(func() { close(cancel) })
	}))
	defer server.Close()

	var logMu sync.Mutex
	var logged []string
	bot := New("123:abc", &recordingAgent{status: "status line"})
	bot.client = newClientAt("123:abc", server.URL)
	bot.Pair(7, 0)
	bot.Log = func(line string) {
		logMu.Lock()
		logged = append(logged, line)
		logMu.Unlock()
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	// The loop stops itself once a second poll has gone through.
	go func() {
		<-cancel
		stop()
	}()

	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		stop()
		t.Fatalf("the loop did not recover from a failed poll")
	}

	mu.Lock()
	total := polls
	mu.Unlock()
	if total < 2 {
		t.Errorf("polls = %d, want the loop to have retried", total)
	}
	logMu.Lock()
	defer logMu.Unlock()
	reported := false
	for _, line := range logged {
		if strings.Contains(line, "poll failed") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("a failed poll should be logged, got %v", logged)
	}
}

// TestRunStopsPromptlyOnCancellation is what /telegram off relies on.
func TestRunStopsPromptlyOnCancellation(t *testing.T) {
	api := newRecordingAPI(t)
	bot := New("123:abc", &recordingAgent{})
	bot.client = api.client()
	bot.Pair(7, 0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()

	// Let it reach the poll, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("cancellation did not stop the loop")
	}
}

// TestRunWaitsForHandlers keeps a stopping bot from leaving work behind.
func TestRunWaitsForHandlers(t *testing.T) {
	api := newRecordingAPI(t)
	release := make(chan struct{})
	agent := &blockingRecordingAgent{release: release}

	var mu sync.Mutex
	polls := 0
	cancel := func() {}
	api.respond = func(method string, payload map[string]any) (any, bool) {
		if method != "getUpdates" {
			return nil, false
		}
		mu.Lock()
		defer mu.Unlock()
		polls++
		if polls == 1 {
			return []any{map[string]any{
				"update_id": 1,
				"message": map[string]any{
					"chat": map[string]any{"id": 7, "type": "private"},
					"from": map[string]any{"id": 9},
					"text": "/status",
				},
			}}, true
		}
		cancel()
		return []any{}, true
	}

	bot := New("123:abc", agent)
	bot.client = api.client()
	bot.Pair(7, 9)

	ctx, stop := context.WithCancel(context.Background())
	cancel = stop
	defer stop()

	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()

	// The handler blocks, so Run must not return while it is in flight.
	select {
	case <-done:
		t.Fatalf("the loop returned while a handler was still running")
	case <-time.After(500 * time.Millisecond):
	}

	close(release)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("the loop never returned after the handler finished")
	}
}

// ------------------------------------------------------------------ routing

// TestHandleUpdateRoutesBothKinds covers the dispatcher that decides what an
// update is.
func TestHandleUpdateRoutesBothKinds(t *testing.T) {
	api := newRecordingAPI(t)
	bot := New("123:abc", &recordingAgent{status: "status line"})
	bot.client = api.client()
	bot.Pair(7, 9)

	bot.handleUpdate(context.Background(), Update{
		UpdateID: 1,
		Message: &Message{
			Chat: Chat{ID: 7, Type: "private"},
			From: &User{ID: 9},
			Text: "/status",
		},
	})
	if !api.called("sendMessage") {
		t.Errorf("a message update should produce a reply")
	}

	bot.handleUpdate(context.Background(), Update{
		UpdateID: 2,
		CallbackQuery: &CallbackQuery{
			ID:      "cb-1",
			From:    &User{ID: 9},
			Message: &Message{Chat: Chat{ID: 7, Type: "private"}},
		},
	})
	if !api.called("answerCallbackQuery") {
		t.Errorf("a callback update should be answered")
	}
}

// TestHandleUpdateSurvivesAPanickingHandler is why the recover exists: one bad
// update must not take the poll loop down with it.
func TestHandleUpdateSurvivesAPanickingHandler(t *testing.T) {
	api := newRecordingAPI(t)

	// A nil agent panics the moment a handler asks it for anything, which is
	// what a partially initialized bot looks like.
	bot := New("123:abc", nil)
	bot.client = api.client()
	bot.Pair(7, 9)

	var logged []string
	bot.Log = func(line string) { logged = append(logged, line) }

	done := make(chan struct{})
	go func() {
		defer close(done)
		bot.handleUpdate(context.Background(), Update{
			UpdateID: 1,
			Message: &Message{
				Chat: Chat{ID: 7, Type: "private"},
				From: &User{ID: 9},
				Text: "/status",
			},
		})
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("handleUpdate did not return after a panicking handler")
	}

	found := false
	for _, line := range logged {
		if strings.Contains(line, "recovered") {
			found = true
		}
	}
	if !found {
		t.Errorf("the panic should be reported, got %v", logged)
	}
}

func TestHandleUpdateIgnoresAnEmptyUpdate(t *testing.T) {
	api := newRecordingAPI(t)
	bot := New("123:abc", &recordingAgent{})
	bot.client = api.client()

	// Neither a message nor a callback: nothing to do, and no panic.
	bot.handleUpdate(context.Background(), Update{UpdateID: 1})
	if api.called("sendMessage") || api.called("answerCallbackQuery") {
		t.Errorf("an empty update should do nothing")
	}
}

func TestHandleCallbackRejectsANonOwner(t *testing.T) {
	api := newRecordingAPI(t)
	bot := New("123:abc", &recordingAgent{})
	bot.client = api.client()
	bot.Pair(7, 9)

	bot.handleCallback(context.Background(), &CallbackQuery{
		ID:      "cb-1",
		From:    &User{ID: 999},
		Message: &Message{Chat: Chat{ID: 7, Type: "private"}},
	})

	calls := api.calls("answerCallbackQuery")
	if len(calls) == 0 {
		t.Fatalf("a callback must still be answered, or the client spins")
	}
	if text, _ := calls[0]["text"].(string); !strings.Contains(text, "Not allowed") {
		t.Errorf("a stranger's callback should be refused, payload = %v", calls[0])
	}
}

func TestHandleCallbackAnswersTheOwner(t *testing.T) {
	api := newRecordingAPI(t)
	bot := New("123:abc", &recordingAgent{})
	bot.client = api.client()
	bot.Pair(7, 9)

	bot.handleCallback(context.Background(), &CallbackQuery{
		ID:      "cb-1",
		From:    &User{ID: 9},
		Message: &Message{Chat: Chat{ID: 7, Type: "private"}},
	})

	calls := api.calls("answerCallbackQuery")
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if _, present := calls[0]["text"]; present {
		t.Errorf("the owner's callback should be answered silently, payload = %v", calls[0])
	}
}

func TestVerifyReturnsTheBotIdentity(t *testing.T) {
	api := newRecordingAPI(t)
	api.respond = func(method string, payload map[string]any) (any, bool) {
		if method != "getMe" {
			return nil, false
		}
		return map[string]any{"id": 5, "username": "check_bot"}, true
	}

	bot := New("123:abc", &recordingAgent{})
	bot.client = api.client()

	user, err := bot.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if user.Username != "check_bot" {
		t.Errorf("user = %+v", user)
	}
}

// ------------------------------------------------------------------ fakes

// recordingAgent records that it was asked for its status.
type recordingAgent struct {
	mu     sync.Mutex
	status string
	calls  int
}

func (a *recordingAgent) RunPrompt(context.Context, string, func(string)) (string, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return "answer", nil
}

func (a *recordingAgent) RunPromptWithImage(context.Context, string, string, string, func(string)) (string, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return "answer", nil
}

func (a *recordingAgent) Stop()                           {}
func (a *recordingAgent) NewSession()                     {}
func (a *recordingAgent) Model() string                   { return "test-model" }
func (a *recordingAgent) SetModel(string) (string, error) { return "test-model", nil }

func (a *recordingAgent) Status() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return a.status
}

func (a *recordingAgent) called() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls > 0
}

// blockingRecordingAgent blocks in Status until released, which is how a test
// puts a handler genuinely in flight.
type blockingRecordingAgent struct {
	release chan struct{}
	once    sync.Once
}

func (a *blockingRecordingAgent) RunPrompt(ctx context.Context, prompt string, progress func(string)) (string, error) {
	return "", fmt.Errorf("unused")
}

func (a *blockingRecordingAgent) RunPromptWithImage(context.Context, string, string, string, func(string)) (string, error) {
	return "", fmt.Errorf("unused")
}

func (a *blockingRecordingAgent) Stop()                           {}
func (a *blockingRecordingAgent) NewSession()                     {}
func (a *blockingRecordingAgent) Model() string                   { return "blocking" }
func (a *blockingRecordingAgent) SetModel(string) (string, error) { return "blocking", nil }

func (a *blockingRecordingAgent) Status() string {
	a.once.Do(func() {})
	<-a.release
	return "released"
}
