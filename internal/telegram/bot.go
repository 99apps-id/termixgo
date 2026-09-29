package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/version"
)

// Agent is the bridge the bot drives. The app implements it over the same
// runner and session the terminal uses.
type Agent interface {
	// RunPrompt runs one turn, calling progress with short status lines.
	RunPrompt(ctx context.Context, prompt string, progress func(string)) (string, error)
	// Stop cancels the running turn.
	Stop()
	// NewSession starts a fresh conversation.
	NewSession()
	// Model returns the current model id.
	Model() string
	// SetModel switches model by id or search text and returns the new id.
	SetModel(query string) (string, error)
	// Status returns a short multi-line status block.
	Status() string
}

// Bot long-polls Telegram and forwards operator messages to the agent.
type Bot struct {
	client *Client
	agent  Agent

	// pairMu guards the pairing state. Every update handler runs on its own
	// goroutine while the app mirrors a new pairing onto the running bot, so an
	// unguarded field here is read by one goroutine as another writes it, which
	// is a data race the detector reports. The state is reached only through
	// the methods below, so an unguarded access no longer compiles.
	pairMu      sync.RWMutex
	chatID      int64
	ownerUserID int64
	pairingCode string

	// OnPaired persists the pairing. It runs on the handler goroutine.
	OnPaired func(chatID, ownerUserID int64)
	// Log receives diagnostic lines; nil discards them.
	Log func(string)

	offset int64

	// handlers tracks in-flight update handlers so shutdown waits for them
	// instead of leaving a goroutine writing to a dead client.
	handlers sync.WaitGroup
	// runMu guards the single agent turn this bot may start. Commands that do
	// not run the agent are deliberately outside it, so /stop and /status are
	// answered while a turn is in flight.
	runMu   sync.Mutex
	running bool
}

// New builds a bot for a token and agent.
func New(token string, agent Agent) *Bot {
	return &Bot{client: NewClient(token), agent: agent}
}

// Paired reports whether the bot is bound to a chat.
func (b *Bot) Paired() bool {
	b.pairMu.RLock()
	defer b.pairMu.RUnlock()
	return b.chatID != 0
}

// Pairing reports the paired chat and owner. A chat of 0 means unpaired.
func (b *Bot) Pairing() (chatID, ownerUserID int64) {
	b.pairMu.RLock()
	defer b.pairMu.RUnlock()
	return b.chatID, b.ownerUserID
}

// Pair binds the bot to a chat and its owner.
func (b *Bot) Pair(chatID, ownerUserID int64) {
	b.pairMu.Lock()
	b.chatID = chatID
	b.ownerUserID = ownerUserID
	b.pairMu.Unlock()
}

// Unpair drops the pairing.
func (b *Bot) Unpair() {
	b.Pair(0, 0)
}

// PairingCode returns the code a /pair message must present.
func (b *Bot) PairingCode() string {
	b.pairMu.RLock()
	defer b.pairMu.RUnlock()
	return b.pairingCode
}

// SetPairingCode records the active pairing code.
func (b *Bot) SetPairingCode(code string) {
	b.pairMu.Lock()
	b.pairingCode = code
	b.pairMu.Unlock()
}

// tryStartRun reserves the single agent turn, reporting false when one is
// already in flight from this bot.
func (b *Bot) tryStartRun() bool {
	b.runMu.Lock()
	defer b.runMu.Unlock()
	if b.running {
		return false
	}
	b.running = true
	return true
}

func (b *Bot) endRun() {
	b.runMu.Lock()
	b.running = false
	b.runMu.Unlock()
}

func (b *Bot) logf(format string, args ...any) {
	if b.Log != nil {
		b.Log(fmt.Sprintf(format, args...))
	}
}

// Verify checks the token and returns the bot identity.
func (b *Bot) Verify(ctx context.Context) (User, error) {
	return b.client.GetMe(ctx)
}

// Run polls until the context is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	// Handlers may outlive a poll iteration; wait for them on the way out so
	// a stopping bot does not keep working in the background.
	defer b.handlers.Wait()

	commands := []BotCommand{
		{Command: "run", Description: "Run a prompt with live progress"},
		{Command: "stop", Description: "Stop the running turn"},
		{Command: "new", Description: "Start a new session"},
		{Command: "status", Description: "Show status"},
		{Command: "model", Description: "Show or switch the model"},
		{Command: "unpair", Description: "Detach this chat"},
		{Command: "help", Description: "List commands"},
	}
	if err := b.client.SetMyCommands(ctx, commands); err != nil {
		b.logf("could not publish the command menu: %v", err)
	}

	backoff := 3 * time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		updates, err := b.client.GetUpdates(ctx, b.offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			backoff = b.handlePollError(ctx, err, backoff)
			continue
		}
		backoff = 3 * time.Second
		for _, update := range updates {
			// Telegram returns updates from the current offset onwards, so an
			// id below it has already been handled. It must be skipped rather
			// than dispatched: running it again would repeat the command.
			// The offset is "the first update to return", not "the last one
			// seen", which is why the comparison is below rather than at.
			if update.UpdateID < b.offset {
				continue
			}
			b.offset = update.UpdateID + 1
			b.dispatch(ctx, update)
		}
	}
}

// dispatch runs one update handler on its own goroutine.
//
// This is what keeps the bot responsive: a prompt runs for minutes, and while
// it held the poll loop the operator could not send /stop, /status or a
// correction, which is precisely when those are needed.
func (b *Bot) dispatch(ctx context.Context, update Update) {
	b.handlers.Add(1)
	go func() {
		defer b.handlers.Done()
		b.handleUpdate(ctx, update)
	}()
}

// handlePollError backs off after a failed poll, honouring Telegram's own
// retry hint and treating a 409 (another poller holds the token) as fatal for
// this loop's cadence.
func (b *Bot) handlePollError(ctx context.Context, err error, backoff time.Duration) time.Duration {
	wait := backoff
	if retry := RetryAfterSeconds(err); retry > 0 {
		wait = time.Duration(retry) * time.Second
	}
	b.logf("poll failed: %v (retrying in %s)", err, wait)
	select {
	case <-ctx.Done():
	case <-time.After(wait):
	}
	next := backoff * 2
	if next > 60*time.Second {
		next = 60 * time.Second
	}
	return next
}

// handleUpdate dispatches one update. A failure is reported to the chat and
// never stops the loop.
func (b *Bot) handleUpdate(ctx context.Context, update Update) {
	defer func() {
		if recovered := recover(); recovered != nil {
			b.logf("recovered from panic while handling an update: %v", recovered)
		}
	}()
	switch {
	case update.CallbackQuery != nil:
		b.handleCallback(ctx, update.CallbackQuery)
	case update.Message != nil:
		b.handleMessage(ctx, update.Message)
	}
}

func (b *Bot) handleCallback(ctx context.Context, query *CallbackQuery) {
	chat := Chat{}
	if query.Message != nil {
		chat = query.Message.Chat
	}
	if !b.isOwner(chat, query.From) {
		_ = b.client.AnswerCallbackQuery(ctx, query.ID, "Not allowed.")
		return
	}
	_ = b.client.AnswerCallbackQuery(ctx, query.ID, "")
}

func (b *Bot) handleMessage(ctx context.Context, message *Message) {
	chatID := message.Chat.ID
	text := strings.TrimSpace(message.Text)

	if !b.Paired() {
		// Unpaired: only /pair is accepted, which is what stops a stranger
		// who finds the bot from running the agent.
		command, argument := splitCommand(text)
		if command == "pair" {
			b.tryPair(ctx, message, argument)
			return
		}
		b.reply(ctx, chatID, "This bot is not paired yet. Send /pair <code> with the code shown in Termixgo.")
		return
	}

	if !b.isOwner(message.Chat, message.From) {
		return
	}

	command, argument := splitCommand(text)
	switch command {
	case "help", "start":
		b.reply(ctx, chatID, helpText())
	case "status":
		b.reply(ctx, chatID, b.agent.Status())
	case "pair":
		b.reply(ctx, chatID, "Already paired. Use /unpair first to move to another chat.")
	case "unpair":
		b.Unpair()
		if b.OnPaired != nil {
			b.OnPaired(0, 0)
		}
		b.reply(ctx, chatID, "Unpaired.")
	case "new":
		b.agent.NewSession()
		b.reply(ctx, chatID, "Started a new session.")
	case "stop":
		b.agent.Stop()
		b.reply(ctx, chatID, "Stopping the running turn.")
	case "model":
		if strings.TrimSpace(argument) == "" {
			b.reply(ctx, chatID, "Model: "+b.agent.Model())
			return
		}
		updated, err := b.agent.SetModel(argument)
		if err != nil {
			b.reply(ctx, chatID, err.Error())
			return
		}
		b.reply(ctx, chatID, "Model is now "+updated+".")
	case "run", "query":
		prompt := strings.TrimSpace(argument)
		if prompt == "" {
			b.reply(ctx, chatID, "Usage: /run <prompt>")
			return
		}
		b.runPrompt(ctx, chatID, prompt)
	default:
		if strings.HasPrefix(text, "/") {
			b.reply(ctx, chatID, "Unknown command. Send /help.")
			return
		}
		// Plain text is a prompt, which is the natural way to use the bot.
		b.runPrompt(ctx, chatID, text)
	}
}

// runPrompt runs one turn and mirrors progress into a single edited message.
func (b *Bot) runPrompt(ctx context.Context, chatID int64, prompt string) {
	if !b.tryStartRun() {
		b.reply(ctx, chatID, "A run is already in progress. Send /stop to stop it, then retry.")
		return
	}
	defer b.endRun()

	statusMessage, err := b.client.SendMessage(ctx, chatID, "Working...", nil)
	if err != nil {
		b.logf("could not send the progress message: %v", err)
	}
	_ = b.client.SendChatAction(ctx, chatID, "typing")

	var lastEdit time.Time
	progress := func(line string) {
		if statusMessage.MessageID == 0 {
			return
		}
		// Throttle edits: Telegram rate-limits rapid edits to one message.
		if time.Since(lastEdit) < 2*time.Second {
			return
		}
		lastEdit = time.Now()
		_ = b.client.EditMessageText(ctx, chatID, statusMessage.MessageID, line)
	}

	answer, runErr := b.agent.RunPrompt(ctx, prompt, progress)
	if statusMessage.MessageID != 0 {
		final := answer
		if runErr != nil {
			final = "Run failed: " + runErr.Error()
		}
		if strings.TrimSpace(final) == "" {
			final = "(no output)"
		}
		_ = b.client.EditMessageText(ctx, chatID, statusMessage.MessageID, final)
		return
	}
	if runErr != nil {
		b.reply(ctx, chatID, "Run failed: "+runErr.Error())
		return
	}
	if strings.TrimSpace(answer) == "" {
		answer = "(no output)"
	}
	b.reply(ctx, chatID, answer)
}

func (b *Bot) tryPair(ctx context.Context, message *Message, code string) {
	expected := b.PairingCode()
	if strings.TrimSpace(expected) == "" {
		b.reply(ctx, message.Chat.ID, "No pairing code is active. Run /setup in Termixgo.")
		return
	}
	if strings.TrimSpace(code) != strings.TrimSpace(expected) {
		b.reply(ctx, message.Chat.ID, "Wrong pairing code.")
		return
	}
	ownerUserID := int64(0)
	if message.From != nil {
		ownerUserID = message.From.ID
	}
	b.Pair(message.Chat.ID, ownerUserID)
	if b.OnPaired != nil {
		b.OnPaired(message.Chat.ID, ownerUserID)
	}
	b.reply(ctx, message.Chat.ID, fmt.Sprintf("Paired. Send any message to run the agent.\n%s", helpText()))
}

// isOwner gates a message. A group chat with no pinned owner fails closed.
func (b *Bot) isOwner(chat Chat, from *User) bool {
	pairedChat, owner := b.Pairing()
	if pairedChat == 0 {
		return false
	}
	if chat.ID != pairedChat {
		return false
	}
	if owner == 0 {
		return chat.IsPrivate()
	}
	return from != nil && from.ID == owner
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if _, err := b.client.SendMessage(ctx, chatID, text, nil); err != nil {
		b.logf("send failed: %v", err)
	}
}

// splitCommand parses "/name argument" or "name argument", tolerating the
// "@botname" suffix Telegram adds to group commands.
func splitCommand(text string) (string, string) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(text), "/")
	command := trimmed
	rest := ""
	if index := strings.IndexAny(trimmed, " \n\t"); index >= 0 {
		command = trimmed[:index]
		rest = strings.TrimSpace(trimmed[index+1:])
	}
	if index := strings.Index(command, "@"); index > 0 {
		command = command[:index]
	}
	return strings.ToLower(command), rest
}

func helpText() string {
	return strings.Join([]string{
		version.Name + " companion bot",
		"",
		"Send any text to run it as a prompt.",
		"/run <prompt>  run with a live progress card",
		"/stop          stop the running turn",
		"/new           start a new session",
		"/model [id]    show or switch the model",
		"/status        show status",
		"/unpair        detach this chat",
		"/help          this list",
	}, "\n")
}
