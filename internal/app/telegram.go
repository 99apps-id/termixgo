package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/secrets"
	"github.com/99apps-id/termixgo/internal/telegram"
)

// TelegramToken returns the stored bot token, or "".
func (a *App) TelegramToken() string {
	return a.store.Get(secrets.TelegramTokenKey())
}

// SetTelegramToken stores the bot token.
func (a *App) SetTelegramToken(token string) error {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return fmt.Errorf("the bot token is empty")
	}
	return a.store.Set(secrets.TelegramTokenKey(), trimmed)
}

// ClearTelegram unpairs the bot, drops the token and stops the loop.
func (a *App) ClearTelegram() error {
	a.StopTelegram()
	if err := a.store.Delete(secrets.TelegramTokenKey()); err != nil {
		return err
	}
	return a.UpdateConfig(func(cfg *config.Config) {
		cfg.Telegram = config.Telegram{}
	})
}

// VerifyTelegramToken checks a token with the Bot API without storing it.
func (a *App) VerifyTelegramToken(ctx context.Context, token string) (string, error) {
	client := telegram.NewClient(token)
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	user, err := client.GetMe(checkCtx)
	if err != nil {
		return "", err
	}
	name := user.Username
	if name == "" {
		name = fmt.Sprintf("%d", user.ID)
	}
	return name, nil
}

// EnsurePairingCode returns the paired-state code, generating one if needed.
func (a *App) EnsurePairingCode() (string, error) {
	a.mu.Lock()
	existing := a.cfg.Telegram.PairingCode
	a.mu.Unlock()
	if strings.TrimSpace(existing) != "" {
		return existing, nil
	}
	return a.RegeneratePairingCode()
}

// RegeneratePairingCode creates a fresh six-digit code.
func (a *App) RegeneratePairingCode() (string, error) {
	code, err := randomCode()
	if err != nil {
		return "", err
	}
	if err := a.UpdateConfig(func(cfg *config.Config) { cfg.Telegram.PairingCode = code }); err != nil {
		return "", err
	}
	a.applyPairingCode(code)
	return code, nil
}

// applyPairingCode hands a new code to a running bot.
//
// Without this a regenerated code is inert: the bot keeps the value it was
// started with, so /pair always fails until the app is restarted.
func (a *App) applyPairingCode(code string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bot != nil {
		a.bot.SetPairingCode(code)
	}
	if code != "" && a.cfg.Telegram.ChatID == 0 {
		a.botStatus = "waiting for /pair"
	}
}

// randomCode returns a zero-padded six-digit code.
func randomCode() (string, error) {
	limit := big.NewInt(1000000)
	value, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", fmt.Errorf("generate a pairing code: %w", err)
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

// SetTelegramEnabled starts or stops the companion loop.
func (a *App) SetTelegramEnabled(enabled bool) error {
	if enabled {
		if strings.TrimSpace(a.TelegramToken()) == "" {
			return fmt.Errorf("add the bot token first")
		}
		if err := a.StartTelegram(); err != nil {
			return err
		}
	} else {
		a.StopTelegram()
	}
	return a.UpdateConfig(func(cfg *config.Config) { cfg.Telegram.Enabled = enabled })
}

// StartTelegram launches the polling loop if it is not already running.
func (a *App) StartTelegram() error {
	a.mu.Lock()
	if a.bot != nil {
		a.mu.Unlock()
		return nil
	}
	token := a.TelegramToken()
	if strings.TrimSpace(token) == "" {
		a.mu.Unlock()
		return fmt.Errorf("no bot token is stored")
	}
	cfg := a.cfg
	a.mu.Unlock()

	bot := telegram.New(token, a)
	bot.Pair(cfg.Telegram.ChatID, cfg.Telegram.OwnerUserID)
	bot.SetPairingCode(cfg.Telegram.PairingCode)
	// The confirmed update id lives beside the rest of the state, scoped to this
	// bot id, so a restart resumes instead of replaying an old prompt. A token
	// without a numeric id has no id to scope by, so no cursor is set rather
	// than sharing one file across bots.
	if id := telegram.BotIDFromToken(token); id != "" {
		if _, err := config.EnsureHome(); err == nil {
			if path, err := config.HomePath("telegram-offset-" + id + ".txt"); err == nil {
				bot.SetCursorPath(path)
			}
		}
	}
	bot.Log = func(line string) {
		a.mu.Lock()
		a.botStatus = "error: " + line
		a.mu.Unlock()
	}
	bot.OnPaired = func(chatID, ownerUserID int64) {
		_ = a.SetTelegramChat(chatID, ownerUserID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.bot = bot
	a.botCancel = cancel
	a.botStatus = "connecting"
	a.mu.Unlock()

	go func() {
		if _, err := bot.Verify(ctx); err != nil {
			a.mu.Lock()
			a.botStatus = "token rejected: " + err.Error()
			a.mu.Unlock()
			cancel()
			a.mu.Lock()
			if a.bot == bot {
				a.bot = nil
			}
			a.botCancel = nil
			a.mu.Unlock()
			return
		}
		a.mu.Lock()
		if bot.Paired() {
			a.botStatus = "paired"
		} else {
			a.botStatus = "waiting for /pair"
		}
		a.mu.Unlock()
		if err := bot.Run(ctx); err != nil && err != context.Canceled {
			a.mu.Lock()
			a.botStatus = "stopped: " + err.Error()
			a.mu.Unlock()
		}
	}()
	return nil
}

// StopTelegram stops the polling loop.
func (a *App) StopTelegram() {
	a.mu.Lock()
	cancel := a.botCancel
	a.bot = nil
	a.botCancel = nil
	a.botStatus = ""
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) SetTelegramChat(chatID, ownerUserID int64) error {
	if err := a.UpdateConfig(func(cfg *config.Config) {
		cfg.Telegram.ChatID = chatID
		cfg.Telegram.OwnerUserID = ownerUserID
		if chatID != 0 {
			cfg.Telegram.PairingCode = ""
			cfg.Telegram.Enabled = true
		}
	}); err != nil {
		return err
	}
	if chatID != 0 {
		a.mu.Lock()
		if a.bot != nil {
			a.bot.Pair(chatID, ownerUserID)
			a.botStatus = "paired"
		}
		a.mu.Unlock()
	}
	return nil
}

// TelegramStatus summarises the bot for the status view.
func (a *App) TelegramStatus() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(a.TelegramToken()) == "" {
		return "off (no token)"
	}
	switch {
	case a.botStatus != "":
		return a.botStatus
	case a.cfg.Telegram.Enabled && a.cfg.Telegram.ChatID != 0:
		return "paired"
	case a.bot != nil:
		return "running"
	default:
		return "configured, stopped"
	}
}

// TelegramChatID returns the paired chat id, or 0.
func (a *App) TelegramChatID() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Telegram.ChatID
}
