// Package channel is the transport-neutral contract the messaging surfaces
// share: one normalized inbound request, the access gate, the pairing store,
// and the text helpers a chat reply needs.
//
// A transport (Telegram, WhatsApp) turns its wire event into a Request; the
// gate answers whether that message may run; the app runs the turn against the
// account's agent. Nothing here imports a transport, so one policy serves all
// of them.
package channel

import "context"

// Request is one inbound message, normalized across transports.
type Request struct {
	// Channel is config.ChannelTelegram or config.ChannelWhatsApp.
	Channel string
	// Account is the account name the message arrived on.
	Account string
	// ChatID identifies the conversation: a Telegram chat id or a WhatsApp
	// JID. It is the key a session is stored under.
	ChatID   string
	ChatName string
	// SenderID identifies the person: a Telegram user id or a WhatsApp JID.
	SenderID   string
	SenderAlt  string
	SenderName string
	IsGroup    bool
	// MentionsBot is true when the message names this account, which is what a
	// group reply requires by default.
	MentionsBot bool
	// Admin is set by the transport after the gate: the operator and the
	// account's admins are admins, everyone else is a guest and runs with the
	// guest tool set and limits.
	Admin bool
	Text  string
}

// AccountID is the registry key of one account.
type AccountID struct {
	Channel string
	Account string
}

// Agent is the app side of one channel account. A bridge is built per account,
// so the account and agent are implicit in the receiver and not repeated here.
type Agent interface {
	// RunPrompt runs one turn and returns the answer text. progress receives
	// short status lines; a transport that shows no working state ignores it.
	RunPrompt(ctx context.Context, request Request, prompt string, progress func(string)) (string, error)
	// NewSession starts a fresh conversation for the request's chat.
	NewSession(request Request)
	Stop()
	Model() string
	SetModel(query string) (string, error)
	Status() string
}
