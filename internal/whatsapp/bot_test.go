package whatsapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
)

func testEvent(text string, mentioned ...string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.NewJID("628123", types.DefaultUserServer),
				Sender: types.NewJID("628999", types.DefaultUserServer),
			},
			PushName: "Budi",
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String(text),
				ContextInfo: &waE2E.ContextInfo{
					MentionedJID: mentioned,
				},
			},
		},
	}
}

func TestMessageText(t *testing.T) {
	if got := messageText(&waE2E.Message{Conversation: proto.String("halo")}); got != "halo" {
		t.Errorf("conversation text = %q", got)
	}
	if got := messageText(nil); got != "" {
		t.Errorf("a nil message has no text, got %q", got)
	}
	image := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("foto")}}
	if got := messageText(image); got != "" {
		t.Errorf("an image has no text to run, got %q", got)
	}
}

func TestRequestFromNormalizesTheMessage(t *testing.T) {
	bot := &Bot{}
	bot.SetAccount("sales", config.ChannelAccount{Enabled: true}, nil)
	event := testEvent("halo", "628111@s.whatsapp.net")
	event.Info.Chat = types.NewJID("12345-67890", types.GroupServer)
	event.Info.IsGroup = true

	request := bot.requestFrom(event)
	if request.Channel != config.ChannelWhatsApp || request.Account != "sales" {
		t.Errorf("request identity = %+v", request)
	}
	if !strings.HasSuffix(request.ChatID, "@g.us") || !request.IsGroup {
		t.Errorf("a group chat must be marked as one: %+v", request)
	}
	if request.SenderID != "628999@s.whatsapp.net" || request.SenderName != "Budi" {
		t.Errorf("sender = %q (%q)", request.SenderID, request.SenderName)
	}
	if request.Text != "halo" {
		t.Errorf("text = %q", request.Text)
	}
	if request.MentionsBot {
		t.Errorf("a mention of another number must not count")
	}
}

func TestMentionsSelfUsesTheLinkedJIDs(t *testing.T) {
	bot := &Bot{}
	bot.SetAccount("sales", config.ChannelAccount{Enabled: true}, nil)
	bot.selfIDs = map[string]bool{channel.CanonicalID("628111@s.whatsapp.net"): true}

	if !bot.mentionsSelf(testEvent("halo @628111", "628111@s.whatsapp.net").Message) {
		t.Errorf("a mention of the linked number must count")
	}
	if bot.mentionsSelf(testEvent("halo @628222", "628222@s.whatsapp.net").Message) {
		t.Errorf("a mention of another number must not count")
	}
	if bot.mentionsSelf(testEvent("halo").Message) {
		t.Errorf("a plain message must not count")
	}
}

func TestNaturalErrorHidesInternals(t *testing.T) {
	busy := naturalError(channel.ErrBusy)
	if !strings.Contains(busy, "Sebentar") {
		t.Errorf("a busy turn must be one friendly line, got %q", busy)
	}
	raw := errors.New("run_command failed: permission denied in /home/secret")
	message := naturalError(raw)
	if strings.Contains(message, "run_command") || strings.Contains(message, "/home/secret") {
		t.Errorf("an internal error leaked into the chat: %q", message)
	}
	if !strings.Contains(message, "gangguan") {
		t.Errorf("the generic failure must still read like a person: %q", message)
	}
	if got := naturalError(context.Canceled); !strings.Contains(got, "dibatalkan") {
		t.Errorf("a cancelled turn reads %q", got)
	}
}
