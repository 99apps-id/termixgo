// Package telegram implements the companion bot: a long-polling Telegram
// client that forwards operator messages to the same agent and reports
// progress back into the chat.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// apiBase is the Telegram Bot API root.
const apiBase = "https://api.telegram.org"

// messageLimit is Telegram's hard cap on a message body.
const messageLimit = 4096

// Client calls the Bot API for one token.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// NewClient builds a client for a bot token.
func NewClient(token string) *Client {
	return newClientAt(token, apiBase)
}

// newClientAt builds a client against a specific API root, which lets tests
// drive the bot without reaching Telegram.
func newClientAt(token, baseURL string) *Client {
	return &Client{
		token:   strings.TrimSpace(token),
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				MaxIdleConns:        4,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 15 * time.Second,
			},
		},
	}
}

// User is the bot identity.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// Chat identifies a conversation.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// Message is an incoming or outgoing message.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

// CallbackQuery is an inline-button press.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// Update is one polled update.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// InlineButton is one keyboard button.
type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

// InlineKeyboard is a keyboard of buttons.
type InlineKeyboard struct {
	InlineKeyboard [][]InlineButton `json:"inline_keyboard"`
}

// BotCommand is one entry in the bot's command menu.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// APIError is a non-ok Bot API reply.
type APIError struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("telegram: %s (%d)", e.Description, e.Code)
	}
	return fmt.Sprintf("telegram returned %d", e.Code)
}

// call performs one Bot API method.
func (c *Client) call(ctx context.Context, method string, payload any, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, method)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	if err != nil {
		return err
	}
	var decoded apiResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("telegram: unreadable reply (%s)", response.Status)
	}
	if !decoded.OK {
		apiErr := &APIError{Code: decoded.ErrorCode, Description: decoded.Description}
		if decoded.Parameters != nil {
			apiErr.RetryAfter = decoded.Parameters.RetryAfter
		}
		return apiErr
	}
	if result != nil && len(decoded.Result) > 0 {
		return json.Unmarshal(decoded.Result, result)
	}
	return nil
}

// GetMe validates the token and returns the bot identity.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var user User
	err := c.call(ctx, "getMe", map[string]any{}, &user)
	return user, err
}

// GetUpdates long-polls for updates. A timeout of 0 returns immediately.
//
// The poll runs under its own deadline so a network stall cannot hang the
// caller's context, which is what keeps the loop recoverable.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSeconds int) ([]Update, error) {
	payload := map[string]any{"timeout": timeoutSeconds, "allowed_updates": []string{"message", "callback_query"}}
	if offset > 0 {
		payload["offset"] = offset
	}
	pollCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds+15)*time.Second)
	defer cancel()
	var updates []Update
	err := c.call(pollCtx, "getUpdates", payload, &updates)
	return updates, err
}

// SendMessage posts a message, optionally with an inline keyboard.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, keyboard *InlineKeyboard) (Message, error) {
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     clampText(text),
		"disable_web_page_preview": true,
	}
	if keyboard != nil && len(keyboard.InlineKeyboard) > 0 {
		payload["reply_markup"] = keyboard
	}
	var message Message
	err := c.call(ctx, "sendMessage", payload, &message)
	return message, err
}

// EditMessageText updates a message in place, which is how the progress card
// avoids flooding the chat.
func (c *Client) EditMessageText(ctx context.Context, chatID int64, messageID int64, text string) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"message_id":               messageID,
		"text":                     clampText(text),
		"disable_web_page_preview": true,
	}
	return c.call(ctx, "editMessageText", payload, nil)
}

// SendChatAction shows a typing indicator.
func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return c.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action}, nil)
}

// AnswerCallbackQuery clears an inline button's spinner.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	payload := map[string]any{"callback_query_id": id}
	if text != "" {
		payload["text"] = text
	}
	return c.call(ctx, "answerCallbackQuery", payload, nil)
}

// SetMyCommands publishes the command menu.
func (c *Client) SetMyCommands(ctx context.Context, commands []BotCommand) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": commands}, nil)
}

// clampText trims a body to Telegram's limit on a line boundary.
func clampText(text string) string {
	if len(text) <= messageLimit {
		return text
	}
	cut := messageLimit - 32
	if index := strings.LastIndex(text[:cut], "\n"); index > cut/2 {
		cut = index
	}
	return text[:cut] + "\n... [truncated]"
}

// IsPrivate reports whether a chat id is a one-to-one chat.
func (chat Chat) IsPrivate() bool { return chat.Type == "private" }

// RetryAfterSeconds extracts the retry hint from an API error.
func RetryAfterSeconds(err error) int {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.RetryAfter
	}
	return 0
}

// BotIDFromToken reads the numeric bot id, which scopes the update offset so
// two bots never share a cursor.
func BotIDFromToken(token string) string {
	if index := strings.Index(token, ":"); index > 0 {
		return token[:index]
	}
	return ""
}
