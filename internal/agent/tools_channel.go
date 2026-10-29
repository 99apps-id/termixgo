package agent

import (
	"context"
	"strings"
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
	return []string{"send_channel_file", "send_channel_message", "send_telegram", "send_whatsapp"}
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
		"channel":   strProp("Target messaging channel: 'telegram' or 'whatsapp'."),
		"file_path": strProp("Optional workspace-relative or absolute file path to send as an attachment (for example '.termixgo/uploads/file.pdf' or 'report.txt')."),
		"message":   strProp("Optional text message or caption to include with the attachment, or standalone message when file_path is omitted."),
		"recipient": strProp("Optional target chat ID, phone number, or WhatsApp JID. Defaults to the configured owner/admin chat for the channel when omitted."),
		"account":   strProp("Optional channel account name. Defaults to 'default'."),
	}, "channel")
}

func (t *sendChannelTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	if env == nil || env.Guest {
		return Result{Output: "sending to messaging channels is restricted to the operator", IsError: true}, nil
	}
	if env.SendChannel == nil {
		return Result{Output: "channel delivery is not available in this environment", IsError: true}, nil
	}
	req := ChannelSendRequest{
		Channel:   strings.TrimSpace(argString(args, "channel")),
		Account:   strings.TrimSpace(argString(args, "account")),
		Recipient: strings.TrimSpace(argString(args, "recipient")),
		Message:   strings.TrimSpace(argString(args, "message")),
		FilePath:  strings.TrimSpace(argString(args, "file_path")),
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
