package telegram

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedAgent is a fully controlled bridge target, which is what the command
// routing needs: every branch has to be reachable on demand.
type scriptedAgent struct {
	mu        sync.Mutex
	status    string
	model     string
	answer    string
	runErr    error
	setErr    error
	prompts   []string
	newCalls  int
	stopCalls int
}

func (a *scriptedAgent) RunPrompt(ctx context.Context, prompt string, progress func(string)) (string, error) {
	a.mu.Lock()
	a.prompts = append(a.prompts, prompt)
	a.mu.Unlock()
	if progress != nil {
		// Twice, so a test can prove the edit throttle works.
		progress("Reading main.go")
		progress("Running tests")
	}
	return a.answer, a.runErr
}

func (a *scriptedAgent) Stop() {
	a.mu.Lock()
	a.stopCalls++
	a.mu.Unlock()
}

func (a *scriptedAgent) NewSession() {
	a.mu.Lock()
	a.newCalls++
	a.mu.Unlock()
}

func (a *scriptedAgent) Model() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func (a *scriptedAgent) SetModel(query string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.setErr != nil {
		return "", a.setErr
	}
	a.model = query
	return query, nil
}

func (a *scriptedAgent) Status() string { return a.status }

func (a *scriptedAgent) firstPrompt() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.prompts) == 0 {
		return ""
	}
	return a.prompts[0]
}

func (a *scriptedAgent) promptCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.prompts)
}

func (a *scriptedAgent) newSessionCalls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.newCalls
}

func (a *scriptedAgent) stopCallsCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopCalls
}

// pairedBot builds a bot already attached to one chat, with a fake API.
func pairedBot(t *testing.T, agent Agent) (*Bot, *recordingAPI) {
	t.Helper()
	api := newRecordingAPI(t)
	bot := New("123:abc", agent)
	bot.client = api.client()
	bot.ChatID = 7
	bot.OwnerUserID = 9
	return bot, api
}

// message is one incoming owner message.
func message(text string) *Message {
	return &Message{
		Chat: Chat{ID: 7, Type: "private"},
		From: &User{ID: 9},
		Text: text,
	}
}

func lastText(t *testing.T, api *recordingAPI, method string) string {
	t.Helper()
	calls := api.calls(method)
	if len(calls) == 0 {
		t.Fatalf("no %s call was made", method)
	}
	text, _ := calls[len(calls)-1]["text"].(string)
	return text
}

// TestUnpairedBotOnlyAcceptsPair is the gate that stops a stranger who finds
// the bot from driving the agent.
func TestUnpairedBotOnlyAcceptsPair(t *testing.T) {
	api := newRecordingAPI(t)
	bot := New("123:abc", &scriptedAgent{})
	bot.client = api.client()
	bot.PairingCode = "123456"

	bot.handleMessage(context.Background(), message("/help"))
	if !strings.Contains(lastText(t, api, "sendMessage"), "not paired yet") {
		t.Errorf("an unpaired bot must refuse other commands, got %q", lastText(t, api, "sendMessage"))
	}

	bot.handleMessage(context.Background(), message("/pair 000000"))
	if !strings.Contains(lastText(t, api, "sendMessage"), "Wrong pairing code") {
		t.Errorf("a wrong code must be refused, got %q", lastText(t, api, "sendMessage"))
	}
	if bot.ChatID != 0 {
		t.Errorf("a wrong code must not pair the chat")
	}

	bot.handleMessage(context.Background(), message("/pair 123456"))
	if bot.ChatID != 7 || bot.OwnerUserID != 9 {
		t.Fatalf("a correct code should pair the owner, got chat %d owner %d", bot.ChatID, bot.OwnerUserID)
	}
	if !strings.Contains(lastText(t, api, "sendMessage"), "Paired.") {
		t.Errorf("reply = %q, want a confirmation", lastText(t, api, "sendMessage"))
	}
}

func TestPairingWithoutACodeIsRefused(t *testing.T) {
	api := newRecordingAPI(t)
	bot := New("123:abc", &scriptedAgent{})
	bot.client = api.client()

	bot.handleMessage(context.Background(), message("/pair 123456"))
	if !strings.Contains(lastText(t, api, "sendMessage"), "No pairing code") {
		t.Errorf("reply = %q, want it to send the operator to /setup", lastText(t, api, "sendMessage"))
	}
	if bot.ChatID != 0 {
		t.Errorf("nothing should have paired")
	}
}

func TestPairingPersistsThroughTheCallback(t *testing.T) {
	api := newRecordingAPI(t)
	bot := New("123:abc", &scriptedAgent{})
	bot.client = api.client()
	bot.PairingCode = "654321"
	var pairedChat, pairedOwner int64
	bot.OnPaired = func(chatID, ownerUserID int64) {
		pairedChat, pairedOwner = chatID, ownerUserID
	}

	bot.handleMessage(context.Background(), message("/pair 654321"))
	if pairedChat != 7 || pairedOwner != 9 {
		t.Errorf("OnPaired got (%d, %d), want (7, 9)", pairedChat, pairedOwner)
	}
}

// TestGroupChatWithNoPinnedOwnerFailsClosed is the safety rule for a chat type
// whose sender identity is not trustworthy: with no pinned owner the bot only
// answers a private chat.
func TestGroupChatWithNoPinnedOwnerFailsClosed(t *testing.T) {
	agent := &scriptedAgent{answer: "secret"}
	bot, api := pairedBot(t, agent)
	bot.OwnerUserID = 0

	bot.handleMessage(context.Background(), &Message{
		Chat: Chat{ID: 7, Type: "group"},
		From: &User{ID: 9},
		Text: "run the agent",
	})
	if api.called("sendMessage") {
		t.Errorf("a group without a pinned owner must not be answered")
	}

	// The same chat as a private one is allowed, because then the sender is
	// verifiable.
	bot.handleMessage(context.Background(), &Message{
		Chat: Chat{ID: 7, Type: "private"},
		From: &User{ID: 9},
		Text: "run the agent",
	})
	if !api.called("sendMessage") {
		t.Errorf("a private chat should be answered")
	}
}

func TestPairedBotSilencesAStranger(t *testing.T) {
	agent := &scriptedAgent{answer: "secret"}
	bot, api := pairedBot(t, agent)

	bot.handleMessage(context.Background(), &Message{
		Chat: Chat{ID: 7, Type: "private"},
		From: &User{ID: 404},
		Text: "/status",
	})
	if api.called("sendMessage") {
		t.Errorf("a stranger must get no reply at all")
	}
	if agent.promptCount() != 0 {
		t.Errorf("a stranger must not reach the agent")
	}

	// A message from another chat is refused too.
	bot.handleMessage(context.Background(), &Message{
		Chat: Chat{ID: 8, Type: "private"},
		From: &User{ID: 9},
		Text: "/status",
	})
	if api.called("sendMessage") {
		t.Errorf("a message from a different chat must be refused")
	}
}

// TestOwnerCommands walks every command the bot answers without running the
// agent, which is what the operator reaches for while a turn is in flight.
func TestOwnerCommands(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		want    string
		check   func(t *testing.T, agent *scriptedAgent, bot *Bot)
		prepare func(bot *Bot)
	}{
		{
			name: "help",
			text: "/help",
			want: "/run <prompt>",
		},
		{
			name: "start is an alias for help",
			text: "/start",
			want: "companion bot",
		},
		{
			name: "status",
			text: "/status",
			want: "the status line",
		},
		{
			name: "already paired",
			text: "/pair anything",
			want: "Already paired",
		},
		{
			name: "model without an argument reports",
			text: "/model",
			want: "Model: test-model",
		},
		{
			name: "model with an argument switches",
			text: "/model gpt-5.4",
			want: "Model is now gpt-5.4.",
			check: func(t *testing.T, agent *scriptedAgent, bot *Bot) {
				if agent.Model() != "gpt-5.4" {
					t.Errorf("model = %q, want the new one", agent.Model())
				}
			},
		},
		{
			name:    "a model that cannot be selected is reported",
			text:    "/model nope",
			want:    "no key for that provider",
			prepare: func(bot *Bot) { bot.agent.(*scriptedAgent).setErr = errors.New("no key for that provider") },
		},
		{
			name: "new session",
			text: "/new",
			want: "Started a new session.",
			check: func(t *testing.T, agent *scriptedAgent, bot *Bot) {
				if agent.newSessionCalls() != 1 {
					t.Errorf("NewSession called %d times, want 1", agent.newSessionCalls())
				}
			},
		},
		{
			name: "stop",
			text: "/stop",
			want: "Stopping the running turn.",
			check: func(t *testing.T, agent *scriptedAgent, bot *Bot) {
				if agent.stopCallsCount() != 1 {
					t.Errorf("Stop called %d times, want 1", agent.stopCallsCount())
				}
			},
		},
		{
			name: "unpair",
			text: "/unpair",
			want: "Unpaired.",
			check: func(t *testing.T, agent *scriptedAgent, bot *Bot) {
				if bot.ChatID != 0 || bot.OwnerUserID != 0 {
					t.Errorf("the pairing should be cleared, got chat %d owner %d", bot.ChatID, bot.OwnerUserID)
				}
			},
		},
		{
			name: "run without a prompt shows usage",
			text: "/run",
			want: "Usage: /run <prompt>",
		},
		{
			name: "an unknown command is named",
			text: "/frobnicate",
			want: "Unknown command",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			agent := &scriptedAgent{status: "the status line", model: "test-model", answer: "done"}
			bot, api := pairedBot(t, agent)
			if testCase.prepare != nil {
				testCase.prepare(bot)
			}

			bot.handleMessage(context.Background(), message(testCase.text))
			got := lastText(t, api, "sendMessage")
			if !strings.Contains(got, testCase.want) {
				t.Errorf("reply = %q, want it to contain %q", got, testCase.want)
			}
			if testCase.check != nil {
				testCase.check(t, agent, bot)
			}
		})
	}
}

// TestBareTextIsAPrompt is the main way the bot is used: no slash, just talk.
func TestBareTextIsAPrompt(t *testing.T) {
	agent := &scriptedAgent{answer: "the answer", model: "test-model"}
	bot, _ := pairedBot(t, agent)

	bot.handleMessage(context.Background(), message("  what does main.go do?  "))

	if got := agent.firstPrompt(); got != "what does main.go do?" {
		t.Errorf("prompt = %q, want it trimmed", got)
	}
}

// TestGroupCommandSuffixIsStripped covers the "@botname" Telegram appends to
// commands in a group, which would otherwise read as an unknown command.
func TestGroupCommandSuffixIsStripped(t *testing.T) {
	command, rest := splitCommand("/model@termixgo_bot gpt-5.4")
	if command != "model" || rest != "gpt-5.4" {
		t.Errorf("splitCommand = (%q, %q)", command, rest)
	}
	if command, _ := splitCommand("/status@termixgo_bot"); command != "status" {
		t.Errorf("command = %q, want status", command)
	}
}

// TestRunPromptMirrorsProgressIntoOneCard walks the progress path: one message
// is sent, progress edits it, and the answer replaces it at the end.
func TestRunPromptMirrorsProgressIntoOneCard(t *testing.T) {
	agent := &scriptedAgent{answer: "the final answer", model: "test-model"}
	bot, api := pairedBot(t, agent)

	bot.handleMessage(context.Background(), message("/run inspect the parser"))

	if got := agent.firstPrompt(); got != "inspect the parser" {
		t.Errorf("prompt = %q", got)
	}
	sent := api.calls("sendMessage")
	if len(sent) != 1 {
		t.Fatalf("sendMessage called %d times, want one progress message", len(sent))
	}
	if sent[0]["text"] != "Working..." {
		t.Errorf("first message = %v, want the placeholder", sent[0]["text"])
	}
	edits := api.calls("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("the progress card was never edited")
	}
	// The throttle drops the second progress line: Telegram rate-limits rapid
	// edits, so only the first within a window is sent.
	if len(edits) != 2 {
		t.Errorf("edits = %d, want one progress edit and one final", len(edits))
	}
	if edits[0]["text"] != "Reading main.go" {
		t.Errorf("first edit = %v, want the first progress line", edits[0]["text"])
	}
	if last := edits[len(edits)-1]["text"]; last != "the final answer" {
		t.Errorf("last edit = %v, want the answer", last)
	}
	if !api.called("sendChatAction") {
		t.Errorf("the typing indicator should be shown")
	}
}

// TestRunPromptSaysSoWhenThereIsNoOutput keeps an empty transcript from
// looking like a message that failed to arrive.
func TestRunPromptSaysSoWhenThereIsNoOutput(t *testing.T) {
	agent := &scriptedAgent{answer: "   "}
	bot, api := pairedBot(t, agent)

	bot.runPrompt(context.Background(), 7, "say nothing")

	edits := api.calls("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("no edit was made")
	}
	if edits[len(edits)-1]["text"] != "(no output)" {
		t.Errorf("final edit = %v, want the placeholder", edits[len(edits)-1]["text"])
	}
}

func TestRunPromptReportsAFailedTurn(t *testing.T) {
	agent := &scriptedAgent{runErr: errors.New("provider is down")}
	bot, api := pairedBot(t, agent)

	bot.runPrompt(context.Background(), 7, "hello")

	edits := api.calls("editMessageText")
	if len(edits) == 0 {
		t.Fatalf("no edit was made")
	}
	final, _ := edits[len(edits)-1]["text"].(string)
	if !strings.Contains(final, "Run failed") || !strings.Contains(final, "provider is down") {
		t.Errorf("final edit = %q, want the failure", final)
	}
}

// TestRunPromptFallsBackToANewMessageWhenTheCardFails covers a client that
// cannot send the progress message at all: the answer must still be delivered.
func TestRunPromptFallsBackToANewMessageWhenTheCardFails(t *testing.T) {
	// A client pointing at a dead port fails every call, which is how the
	// send-a-card path is skipped.
	bot := New("123:abc", &scriptedAgent{answer: "still delivered"})
	bot.client = newClientAt("123:abc", "http://127.0.0.1:9")
	bot.ChatID = 7
	bot.OwnerUserID = 9

	var logged []string
	bot.Log = func(line string) { logged = append(logged, line) }

	// Every call fails, so the only observable effect is the log line and that
	// the call returns rather than hanging.
	done := make(chan struct{})
	go func() {
		defer close(done)
		bot.runPrompt(context.Background(), 7, "hello")
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("a failing client must not hang the prompt")
	}
	if len(logged) == 0 {
		t.Errorf("a failed send should be logged")
	}
}

func TestRunPromptRefusesASecondRun(t *testing.T) {
	agent := &scriptedAgent{answer: "done"}
	bot, api := pairedBot(t, agent)
	bot.running = true

	bot.runPrompt(context.Background(), 7, "hello")

	got := lastText(t, api, "sendMessage")
	if !strings.Contains(got, "already in progress") {
		t.Errorf("reply = %q, want it to refuse", got)
	}
	if agent.promptCount() != 0 {
		t.Errorf("the agent must not have been asked to run")
	}
}

func TestReplyLogsAFailureWithoutPanicking(t *testing.T) {
	bot := New("123:abc", &scriptedAgent{})
	bot.client = newClientAt("123:abc", "http://127.0.0.1:9")

	var logged []string
	bot.Log = func(line string) { logged = append(logged, line) }
	bot.reply(context.Background(), 7, "hello")

	if len(logged) == 0 {
		t.Errorf("a failed reply should be logged")
	}
	// A nil logger must be tolerated, because the bot runs without one in the
	// plain CLI.
	bot.Log = nil
	bot.reply(context.Background(), 7, "hello")
}
