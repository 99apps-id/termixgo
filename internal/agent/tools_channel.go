package agent

import (
	"context"
	"strings"

	"github.com/99apps-id/termixgo/internal/channel"
)

// ChannelSendRequest describes a message and/or file attachment to deliver to a
// messaging channel (Telegram or WhatsApp).
type ChannelSendRequest struct {
	Channel   string
	Account   string
	Recipient string
	Message   string
	FilePath  string
}

type sendChannelTool struct{}

func (t *sendChannelTool) Name() string { return "send_channel" }

func (t *sendChannelTool) Aliases() []string {
	return []string{
		"send_file", "send_document",
		"send_channel_file", "send_channel_message",
		"send_telegram", "send_whatsapp",
		"telegram_send_document", "telegram_send_message",
		"whatsapp_send_document", "whatsapp_send_message",
	}
}

func (t *sendChannelTool) Mutating() bool { return false }

func (t *sendChannelTool) Risk() Risk { return RiskNetwork }

func (t *sendChannelTool) Label(a map[string]any) string {
	ch := strings.TrimSpace(argString(a, "channel"))
	if ch == "" {
		ch = "channel"
	}
	if fp := strings.TrimSpace(argString(a, "file_path")); fp != "" {
		return "Sending " + Shorten(fp, 32) + " to " + ch
	}
	return "Sending message to " + ch
}

func (t *sendChannelTool) DoneLabel(a map[string]any) string {
	ch := strings.TrimSpace(argString(a, "channel"))
	if ch == "" {
		ch = "channel"
	}
	if fp := strings.TrimSpace(argString(a, "file_path")); fp != "" {
		return "Sent " + Shorten(fp, 32) + " to " + ch
	}
	return "Sent message to " + ch
}

func (t *sendChannelTool) Description() string {
	return "Send a file attachment (such as a document or image from the workspace or .termixgo/uploads/) and/or a text message to Telegram or WhatsApp."
}

func (t *sendChannelTool) Schema() map[string]any {
	return object(map[string]any{
		"channel":   strProp("Target messaging channel: 'telegram' or 'whatsapp'. When invoked inside a Telegram or WhatsApp chat, defaults to the current channel."),
		"file_path": strProp("Workspace-relative or absolute file path to send as an attachment (e.g. '.termixgo/uploads/file.pdf' or 'report.xlsx'). Also accepts 'path' or 'file'."),
		"message":   strProp("Optional text message or caption to include with the attachment, or standalone message when file_path is omitted. Also accepts 'caption' or 'text'."),
		"recipient": strProp("Optional target chat ID, phone number, or WhatsApp JID. Defaults to the current chat for guests or the configured owner/admin chat."),
		"account":   strProp("Optional channel account name. Defaults to the current caller account or 'default'."),
	})
}

func (t *sendChannelTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if env == nil {
		return Result{Output: "no environment available", IsError: true}, nil
	}
	filePath := strings.TrimSpace(argString(args, "file_path"))
	if filePath == "" {
		filePath = strings.TrimSpace(argString(args, "path"))
	}
	if filePath == "" {
		filePath = strings.TrimSpace(argString(args, "file"))
	}
	if filePath == "" {
		filePath = strings.TrimSpace(argString(args, "document"))
	}
	message := strings.TrimSpace(argString(args, "message"))
	if message == "" {
		message = strings.TrimSpace(argString(args, "caption"))
	}
	if message == "" {
		message = strings.TrimSpace(argString(args, "text"))
	}
	req := ChannelSendRequest{
		Channel:   strings.TrimSpace(argString(args, "channel")),
		Account:   strings.TrimSpace(argString(args, "account")),
		Recipient: strings.TrimSpace(argString(args, "recipient")),
		Message:   message,
		FilePath:  filePath,
	}
	if req.Channel == "" && env.CallerChannel != "" {
		req.Channel = strings.ToLower(env.CallerChannel)
	}
	if req.Account == "" && env.CallerAccount != "" {
		req.Account = env.CallerAccount
	}
	if env.Guest {
		if env.CallerChatID == "" {
			return Result{Output: "sending to messaging channels is restricted to the operator", IsError: true}, nil
		}
		cleanRecip := channel.CanonicalID(req.Recipient)
		cleanCaller := channel.CanonicalID(env.CallerChatID)
		if cleanRecip != "" && cleanRecip != cleanCaller {
			return Result{Output: "guests may only receive files in their own chat", IsError: true}, nil
		}
		req.Recipient = env.CallerChatID
		if req.Channel == "" {
			req.Channel = strings.ToLower(env.CallerChannel)
		}
		if req.Account == "" {
			req.Account = env.CallerAccount
		}
	} else if req.Recipient == "" && env.CallerChatID != "" {
		req.Recipient = env.CallerChatID
	}
	if env.SendChannel == nil {
		return Result{Output: "channel delivery is not available in this environment", IsError: true}, nil
	}
	if req.Channel == "" {
		return Result{Output: "channel is required ('telegram' or 'whatsapp')", IsError: true}, nil
	}
	if req.FilePath == "" && req.Message == "" {
		return Result{Output: "provide at least one of file_path or message", IsError: true}, nil
	}
	summary, err := env.SendChannel(ctx, req)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: summary}, nil
}
