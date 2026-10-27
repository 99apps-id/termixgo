package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
)

func TestMentionsBot(t *testing.T) {
	bot := New("123:abc", nil)
	bot.setIdentity(User{ID: 99, Username: "termixgo_bot"})

	mention := &Message{
		Text:     "@termixgo_bot tolong cek",
		Entities: []MessageEntity{{Type: "mention", Offset: 0, Length: 13}},
	}
	if !bot.mentionsBot(mention) {
		t.Errorf("an @username mention must count")
	}
	textMention := &Message{
		Text:     "tolong cek",
		Entities: []MessageEntity{{Type: "text_mention", User: &User{ID: 99}}},
	}
	if !bot.mentionsBot(textMention) {
		t.Errorf("a text_mention of the bot must count")
	}
	reply := &Message{Text: "ya", ReplyToMessage: &Message{From: &User{ID: 99}}}
	if !bot.mentionsBot(reply) {
		t.Errorf("a reply to the bot must count")
	}
	plain := &Message{Text: "halo semua"}
	if bot.mentionsBot(plain) {
		t.Errorf("a plain group message must not count")
	}
	otherMention := &Message{
		Text:     "@someone else",
		Entities: []MessageEntity{{Type: "mention", Offset: 1, Length: 9}},
	}
	if bot.mentionsBot(otherMention) {
		t.Errorf("a mention of another user must not count")
	}
}

func TestGuestCommandIsRefusedPolitely(t *testing.T) {
	agent := &scriptedAgent{answer: "secret"}
	bot, api := pairedBot(t, agent)
	bot.SetAccount("default", config.ChannelAccount{
		Enabled:   true,
		DMPolicy:  config.DMPolicyAllowlist,
		AllowFrom: []string{"42"},
	}, nil)

	bot.handleMessage(context.Background(), &Message{
		Chat: Chat{ID: 7, Type: "private"},
		From: &User{ID: 42},
		Text: "/model gpt-5",
	})
	if agent.promptCount() != 0 {
		t.Errorf("a guest must not reach the agent through an admin command")
	}
	if reply := lastText(t, api, "sendMessage"); !strings.Contains(reply, "hanya untuk operator") {
		t.Errorf("the refusal must be polite and explicit, got %q", reply)
	}
}

func TestPairingPolicyAnswersWithACode(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	path, err := channel.DefaultPairingPath()
	if err != nil {
		t.Fatalf("DefaultPairingPath: %v", err)
	}
	store, err := channel.OpenPairingStore(path)
	if err != nil {
		t.Fatalf("OpenPairingStore: %v", err)
	}
	agent := &scriptedAgent{answer: "secret"}
	bot, api := pairedBot(t, agent)
	bot.SetAccount("default", config.ChannelAccount{
		Enabled:     true,
		DMPolicy:    config.DMPolicyPairing,
		GroupPolicy: config.GroupPolicyMention,
	}, store)

	bot.handleMessage(context.Background(), &Message{
		Chat: Chat{ID: 8, Type: "private"},
		From: &User{ID: 55},
		Text: "halo",
	})
	text := lastText(t, api, "sendMessage")
	if !strings.Contains(text, "Sandi pairing") {
		t.Fatalf("an unknown DM must receive a pairing code, got %q", text)
	}
	if agent.promptCount() != 0 {
		t.Errorf("the agent must not run before the operator approves")
	}
	if pending := store.Pending(); len(pending) != 1 || pending[0].SenderID != "55" {
		t.Errorf("the request was not recorded: %+v", pending)
	}
}

func TestGuestRateLimitRefusesTheSecondMessage(t *testing.T) {
	agent := &scriptedAgent{answer: "ok"}
	bot, api := pairedBot(t, agent)
	bot.SetAccount("default", config.ChannelAccount{
		Enabled:   true,
		DMPolicy:  config.DMPolicyAllowlist,
		AllowFrom: []string{"42"},
		Guest:     &config.GuestLimits{RatePerMinute: 1},
	}, nil)

	message := &Message{Chat: Chat{ID: 7, Type: "private"}, From: &User{ID: 42}, Text: "halo"}
	bot.handleMessage(context.Background(), message)
	bot.handleMessage(context.Background(), message)

	if agent.promptCount() != 1 {
		t.Errorf("the second message must be throttled, prompts = %d", agent.promptCount())
	}
	if reply := lastText(t, api, "sendMessage"); !strings.Contains(reply, "Terlalu banyak") {
		t.Errorf("a throttled guest must be told so, got %q", reply)
	}
}
