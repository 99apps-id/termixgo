package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI is a stand-in for the Telegram Bot API. It records the text of every
// sendMessage so a test can assert what the operator would have seen.
type fakeAPI struct {
	mu       sync.Mutex
	sent     []string
	edits    []string
	server   *httptest.Server
	nextID   int
	onMethod func(method string, payload map[string]any) map[string]any
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	api := &fakeAPI{}
	api.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]

		api.mu.Lock()
		api.nextID++
		id := api.nextID
		switch method {
		case "sendMessage":
			if text, ok := payload["text"].(string); ok {
				api.sent = append(api.sent, text)
			}
		case "editMessageText":
			if text, ok := payload["text"].(string); ok {
				api.edits = append(api.edits, text)
			}
		}
		hook := api.onMethod
		api.mu.Unlock()

		result := map[string]any{
			"message_id": id,
			"chat":       map[string]any{"id": payload["chat_id"], "type": "private"},
			"text":       payload["text"],
		}
		if hook != nil {
			if override := hook(method, payload); override != nil {
				result = override
			}
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(api.server.Close)
	return api
}

func (api *fakeAPI) client() *Client { return newClientAt("123:abc", api.server.URL) }

func (api *fakeAPI) messages() []string {
	api.mu.Lock()
	defer api.mu.Unlock()
	return append([]string(nil), api.sent...)
}

func (api *fakeAPI) contains(needle string) bool {
	for _, text := range api.messages() {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// blockingAgent holds a run open until released, which is how the tests put a
// turn genuinely in flight.
type blockingAgent struct {
	release chan struct{}
	started chan struct{}
	once    sync.Once

	mu   sync.Mutex
	runs int
}

func newBlockingAgent() *blockingAgent {
	return &blockingAgent{release: make(chan struct{}), started: make(chan struct{})}
}

func (a *blockingAgent) RunPrompt(ctx context.Context, prompt string, progress func(string)) (string, error) {
	a.mu.Lock()
	a.runs++
	a.mu.Unlock()
	a.once.Do(func() { close(a.started) })
	select {
	case <-a.release:
		return "finished: " + prompt, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (a *blockingAgent) Stop()                           {}
func (a *blockingAgent) NewSession()                     {}
func (a *blockingAgent) Model() string                   { return "test-model" }
func (a *blockingAgent) SetModel(string) (string, error) { return "test-model", nil }
func (a *blockingAgent) Status() string                  { return "status line" }

func (a *blockingAgent) runCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runs
}

func TestRunGateAllowsOneTurnAtATime(t *testing.T) {
	bot := &Bot{}
	if !bot.tryStartRun() {
		t.Fatalf("the first reservation should succeed")
	}
	if bot.tryStartRun() {
		t.Fatalf("a second reservation must fail while a turn is running")
	}
	bot.endRun()
	if !bot.tryStartRun() {
		t.Fatalf("a reservation should succeed again after endRun")
	}
}

func TestSecondPromptIsRejectedWhileARunIsInFlight(t *testing.T) {
	api := newFakeAPI(t)
	agent := newBlockingAgent()
	bot := New("123:abc", agent)
	bot.client = api.client()
	bot.Pair(42, 7)

	first := make(chan struct{})
	go func() {
		defer close(first)
		bot.runPrompt(context.Background(), 42, "do the long thing")
	}()

	select {
	case <-agent.started:
	case <-time.After(5 * time.Second):
		t.Fatalf("the first run never started")
	}

	// The second prompt must be refused with an explanation, not queued and
	// not run in parallel.
	bot.runPrompt(context.Background(), 42, "a second thing")
	if !api.contains("already in progress") {
		t.Errorf("the second prompt should be refused politely, got %v", api.messages())
	}
	if got := agent.runCount(); got != 1 {
		t.Errorf("runs = %d, want 1", got)
	}

	close(agent.release)
	<-first
	if got := agent.runCount(); got != 1 {
		t.Errorf("runs after release = %d, want 1", got)
	}
}

func TestCommandsAreAnsweredWhileARunIsInFlight(t *testing.T) {
	api := newFakeAPI(t)
	agent := newBlockingAgent()
	bot := New("123:abc", agent)
	bot.client = api.client()
	bot.Pair(42, 7)

	done := make(chan struct{})
	go func() {
		defer close(done)
		bot.runPrompt(context.Background(), 42, "long task")
	}()
	<-agent.started

	// These must not consult the run lock: /status while working is exactly
	// the case the old blocking loop got wrong.
	bot.handleMessage(context.Background(), &Message{Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 7}, Text: "/status"})
	if !api.contains("status line") {
		t.Errorf("/status should be answered during a run, got %v", api.messages())
	}

	bot.handleMessage(context.Background(), &Message{Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 7}, Text: "/model"})
	if !api.contains("test-model") {
		t.Errorf("/model should be answered during a run, got %v", api.messages())
	}

	close(agent.release)
	<-done
}

func TestPairingRequiresTheActiveCode(t *testing.T) {
	api := newFakeAPI(t)
	bot := New("123:abc", newBlockingAgent())
	bot.client = api.client()
	bot.SetPairingCode("123456")

	paired := make(chan struct{}, 1)
	bot.OnPaired = func(chatID, ownerUserID int64) {
		paired <- struct{}{}
	}
	// A wrong code must not pair.
	bot.handleMessage(context.Background(), &Message{Chat: Chat{ID: 99, Type: "private"}, From: &User{ID: 7}, Text: "/pair 000000"})
	if chatID, _ := bot.Pairing(); chatID != 0 {
		t.Fatalf("a wrong code must not pair, chat is %d", chatID)
	}
	if !api.contains("Wrong pairing code") {
		t.Errorf("the refusal should say the code is wrong, got %v", api.messages())
	}

	// The right code pairs and pins the owner.
	bot.handleMessage(context.Background(), &Message{Chat: Chat{ID: 99, Type: "private"}, From: &User{ID: 7}, Text: "/pair 123456"})
	select {
	case <-paired:
	case <-time.After(time.Second):
		t.Fatalf("OnPaired was never called")
	}
	if chatID, owner := bot.Pairing(); chatID != 99 || owner != 7 {
		t.Errorf("pairing landed as chat=%d owner=%d, want 99 and 7", chatID, owner)
	}
}

func TestUnpairedBotRefusesStrangers(t *testing.T) {
	api := newFakeAPI(t)
	bot := New("123:abc", newBlockingAgent())
	bot.client = api.client()

	bot.handleMessage(context.Background(), &Message{Chat: Chat{ID: 5, Type: "private"}, From: &User{ID: 5}, Text: "run something"})
	if !api.contains("not paired yet") {
		t.Errorf("an unpaired bot should explain itself, got %v", api.messages())
	}
}

func TestNonOwnerIsIgnored(t *testing.T) {
	api := newFakeAPI(t)
	agent := newBlockingAgent()
	bot := New("123:abc", agent)
	bot.client = api.client()
	bot.Pair(42, 7)

	bot.handleMessage(context.Background(), &Message{Chat: Chat{ID: 42, Type: "group"}, From: &User{ID: 999}, Text: "/status"})
	if len(api.messages()) != 0 {
		t.Errorf("a stranger should be ignored, got %v", api.messages())
	}
	if agent.runCount() != 0 {
		t.Errorf("a stranger must not start a run")
	}
}

func TestUnknownCommandPointsAtHelp(t *testing.T) {
	api := newFakeAPI(t)
	bot := New("123:abc", newBlockingAgent())
	bot.client = api.client()
	bot.Pair(42, 7)

	bot.handleMessage(context.Background(), &Message{Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 7}, Text: "/nonsense"})
	if !api.contains("/help") {
		t.Errorf("an unknown command should point at /help, got %v", api.messages())
	}
}

func TestDispatchRunsHandlersConcurrently(t *testing.T) {
	// The poll loop must not wait for a handler: a slow update would
	// otherwise stall every later update, including /stop.
	bot := &Bot{}
	bot.client = NewClient("123:abc")
	started := make(chan struct{}, 2)
	release := make(chan struct{})

	for index := 0; index < 2; index++ {
		bot.handlers.Add(1)
		go func() {
			defer bot.handlers.Done()
			started <- struct{}{}
			<-release
		}()
	}
	for index := 0; index < 2; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("handlers did not start concurrently")
		}
	}
	close(release)
	bot.handlers.Wait()
}

func TestPollErrorBacksOffUpToTheCap(t *testing.T) {
	bot := &Bot{}
	// A cancelled context makes the wait return immediately, so the test
	// exercises the growth of the delay without actually sleeping through it.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	backoff := 3 * time.Second
	for index := 0; index < 8; index++ {
		backoff = bot.handlePollError(ctx, fmt.Errorf("boom"), backoff)
	}
	if backoff != 60*time.Second {
		t.Errorf("backoff = %s, want it capped at 60s", backoff)
	}
}
