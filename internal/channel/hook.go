package channel

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

// Hook event types.
const (
	EventInboundMessage = "inbound_message"
	EventTurnCompleted  = "turn_completed"
	EventPing           = "ping"
)

// Hook actions.
const (
	ActionContinue = "continue"
	ActionReply    = "reply"
	ActionModify   = "modify"
	ActionDrop     = "drop"
	ActionIgnore   = "ignore"
)

// HookPayload is the JSON payload sent to the webhook interceptor endpoint.
type HookPayload struct {
	Event       string         `json:"event"`
	Channel     string         `json:"channel"`
	Account     string         `json:"account"`
	ChatID      string         `json:"chat_id"`
	ChatName    string         `json:"chat_name,omitempty"`
	SenderID    string         `json:"sender_id"`
	SenderAlt   string         `json:"sender_alt,omitempty"`
	SenderName  string         `json:"sender_name,omitempty"`
	IsGroup     bool           `json:"is_group"`
	MentionsBot bool           `json:"mentions_bot"`
	Admin       bool           `json:"admin"`
	Text        string         `json:"text"`
	Prompt      string         `json:"prompt"`
	MediaType   string         `json:"media_type,omitempty"`
	Answer      string         `json:"answer,omitempty"`
	Timestamp   int64          `json:"timestamp"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// HookResponse is the JSON response returned by the webhook interceptor.
type HookResponse struct {
	// Action determines the subsequent execution flow:
	// - "continue" (default): forward prompt to the LLM.
	// - "reply": bypass LLM and immediately send Reply text to the chat.
	// - "modify": update Prompt before forwarding to the LLM.
	// - "drop" or "ignore": discard the message without LLM or reply.
	Action   string         `json:"action,omitempty"`
	Prompt   string         `json:"prompt,omitempty"`
	Reply    string         `json:"reply,omitempty"`
	Silent   bool           `json:"silent,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// MessageInterceptor is an in-process Go callback that can intercept
// inbound channel messages before LLM execution.
type MessageInterceptor func(ctx context.Context, payload HookPayload) (*HookResponse, error)

var (
	globalInterceptorMu sync.RWMutex
	globalInterceptor   MessageInterceptor

	// Default HTTP client with timeout for webhook invocations.
	hookHTTPClient = &http.Client{
		Timeout: 10 * time.Second,
	}
)

// SetGlobalInterceptor registers a process-wide native Go interceptor
// that intercepts messages across all channels and accounts.
func SetGlobalInterceptor(interceptor MessageInterceptor) {
	globalInterceptorMu.Lock()
	defer globalInterceptorMu.Unlock()
	globalInterceptor = interceptor
}

// GlobalInterceptor returns the registered global interceptor or nil.
func GlobalInterceptor() MessageInterceptor {
	globalInterceptorMu.RLock()
	defer globalInterceptorMu.RUnlock()
	return globalInterceptor
}

// ResolveHookTarget resolves the effective webhook URL and secret for an
// account, checking the account policy, fallback channels config, and environment.
func ResolveHookTarget(account config.ChannelAccount, fallbackURL, fallbackSecret string) (string, string) {
	url := strings.TrimSpace(account.HookURL)
	if url == "" {
		url = strings.TrimSpace(fallbackURL)
	}
	if url == "" {
		url = strings.TrimSpace(os.Getenv("TERMIXGO_CHANNEL_HOOK_URL"))
	}
	if url == "" {
		url = strings.TrimSpace(os.Getenv("TERMIXGO_BOT_HOOK_URL"))
	}

	secret := strings.TrimSpace(account.HookSecret)
	if secret == "" {
		secret = strings.TrimSpace(fallbackSecret)
	}
	if secret == "" {
		secret = strings.TrimSpace(os.Getenv("TERMIXGO_CHANNEL_HOOK_SECRET"))
	}
	if secret == "" {
		secret = strings.TrimSpace(os.Getenv("TERMIXGO_BOT_HOOK_SECRET"))
	}
	return url, secret
}

// PostHTTPHook sends an HTTP POST request containing HookPayload to hookURL.
func PostHTTPHook(ctx context.Context, hookURL, secret string, payload HookPayload) (*HookResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal hook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hookURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create hook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Termixgo-Native-Hook/1.0")
	req.Header.Set("X-Termixgo-Event", payload.Event)
	req.Header.Set("X-Termixgo-Channel", payload.Channel)
	req.Header.Set("X-Termixgo-Account", payload.Account)

	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Termixgo-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := hookHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute hook request to %s: %w", hookURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return &HookResponse{Action: ActionContinue}, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("hook returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("read hook response: %w", err)
	}

	trimmed := bytes.TrimSpace(rawResp)
	if len(trimmed) == 0 {
		return &HookResponse{Action: ActionContinue}, nil
	}

	var parsed HookResponse
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return nil, fmt.Errorf("decode hook response: %w", err)
	}

	// Normalize intuitive responses
	act := strings.ToLower(strings.TrimSpace(parsed.Action))
	if act == "" {
		if strings.TrimSpace(parsed.Reply) != "" {
			act = ActionReply
		} else if strings.TrimSpace(parsed.Prompt) != "" {
			act = ActionModify
		} else {
			act = ActionContinue
		}
	}
	parsed.Action = act
	return &parsed, nil
}

// InvokeInboundHook invokes either the custom interceptor, the global interceptor,
// or the configured HTTP webhook endpoint for an incoming message.
func InvokeInboundHook(
	ctx context.Context,
	interceptor MessageInterceptor,
	hookURL, secret string,
	req Request,
	prompt, mediaType string,
) (*HookResponse, error) {
	payload := HookPayload{
		Event:       EventInboundMessage,
		Channel:     req.Channel,
		Account:     req.Account,
		ChatID:      req.ChatID,
		ChatName:    req.ChatName,
		SenderID:    req.SenderID,
		SenderAlt:   req.SenderAlt,
		SenderName:  req.SenderName,
		IsGroup:     req.IsGroup,
		MentionsBot: req.MentionsBot,
		Admin:       req.Admin,
		Text:        req.Text,
		Prompt:      prompt,
		MediaType:   mediaType,
		Timestamp:   time.Now().Unix(),
	}

	if interceptor != nil {
		return interceptor(ctx, payload)
	}

	if global := GlobalInterceptor(); global != nil {
		return global(ctx, payload)
	}

	if strings.TrimSpace(hookURL) != "" {
		return PostHTTPHook(ctx, hookURL, secret, payload)
	}

	return nil, nil
}

// NotifyTurnCompleted fires a turn_completed event to the interceptor or webhook
// after the LLM finishes responding.
func NotifyTurnCompleted(
	ctx context.Context,
	interceptor MessageInterceptor,
	hookURL, secret string,
	req Request,
	prompt, answer string,
) {
	payload := HookPayload{
		Event:       EventTurnCompleted,
		Channel:     req.Channel,
		Account:     req.Account,
		ChatID:      req.ChatID,
		ChatName:    req.ChatName,
		SenderID:    req.SenderID,
		SenderAlt:   req.SenderAlt,
		SenderName:  req.SenderName,
		IsGroup:     req.IsGroup,
		MentionsBot: req.MentionsBot,
		Admin:       req.Admin,
		Text:        req.Text,
		Prompt:      prompt,
		Answer:      answer,
		Timestamp:   time.Now().Unix(),
	}

	if interceptor != nil {
		_, _ = interceptor(ctx, payload)
		return
	}

	if global := GlobalInterceptor(); global != nil {
		_, _ = global(ctx, payload)
		return
	}

	if trimmed := strings.TrimSpace(hookURL); trimmed != "" {
		// Non-blocking fire-and-forget notification with fresh timeout
		go func() {
			notifyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = PostHTTPHook(notifyCtx, trimmed, secret, payload)
		}()
	}
}

// PingHTTPHook checks connectivity to a webhook URL by sending a ping event.
func PingHTTPHook(ctx context.Context, hookURL, secret string) (time.Duration, error) {
	start := time.Now()
	payload := HookPayload{
		Event:     EventPing,
		Timestamp: start.Unix(),
		Text:      "termixgo ping",
	}
	_, err := PostHTTPHook(ctx, hookURL, secret, payload)
	duration := time.Since(start)
	return duration, err
}
