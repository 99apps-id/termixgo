package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// bound builds a bot already attached to a chat, which is the state most
// handler tests want.
func bound(chatID, ownerUserID int64) *Bot {
	bot := &Bot{}
	bot.Pair(chatID, ownerUserID)
	return bot
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		input   string
		command string
		args    string
	}{
		{"/run fix the bug", "run", "fix the bug"},
		{"/status", "status", ""},
		{"/model@TermixgoBot claude-sonnet-4-5", "model", "claude-sonnet-4-5"},
		{"  /Stop  ", "stop", ""},
		{"plain text", "plain", "text"},
		{"/run", "run", ""},
	}
	for _, testCase := range cases {
		command, args := splitCommand(testCase.input)
		if command != testCase.command || args != testCase.args {
			t.Errorf("splitCommand(%q) = (%q, %q), want (%q, %q)",
				testCase.input, command, args, testCase.command, testCase.args)
		}
	}
}

func TestOwnerGating(t *testing.T) {
	bot := bound(100, 7)

	if bot.isOwner(Chat{ID: 100, Type: "private"}, &User{ID: 8}) {
		t.Errorf("a different user id must not be the owner")
	}
	if !bot.isOwner(Chat{ID: 100, Type: "private"}, &User{ID: 7}) {
		t.Errorf("the pinned owner should be allowed")
	}
	if bot.isOwner(Chat{ID: 200, Type: "private"}, &User{ID: 7}) {
		t.Errorf("another chat must not be allowed")
	}
}

func TestUnpairedBotRejectsEverything(t *testing.T) {
	bot := &Bot{}
	if bot.isOwner(Chat{ID: 5, Type: "private"}, &User{ID: 5}) {
		t.Fatalf("an unpaired bot must not accept messages")
	}
}

func TestGroupChatWithoutOwnerFailsClosed(t *testing.T) {
	bot := bound(-100, 0)
	if bot.isOwner(Chat{ID: -100, Type: "group"}, &User{ID: 7}) {
		t.Fatalf("a group chat with no pinned owner must fail closed")
	}
	bot.Pair(-100, 7)
	if !bot.isOwner(Chat{ID: -100, Type: "group"}, &User{ID: 7}) {
		t.Fatalf("a pinned owner in a group should be allowed")
	}
}

func TestClampTextTrimsToLimit(t *testing.T) {
	long := make([]rune, messageLimit+500)
	for index := range long {
		long[index] = 'a'
	}
	clamped := clampText(string(long))
	if len(clamped) > messageLimit {
		t.Errorf("clamped length = %d, want at most %d", len(clamped), messageLimit)
	}
	if short := clampText("ok"); short != "ok" {
		t.Errorf("short text should pass through, got %q", short)
	}
}

// TestClampTextKeepsValidUTF8 covers a message that would otherwise never
// arrive. A Telegram body is free text and often holds emoji or non-Latin
// script; a byte slice through the middle of one produces invalid UTF-8, and the
// Bot API rejects the whole request rather than truncating it.
func TestClampTextKeepsValidUTF8(t *testing.T) {
	for _, filler := range []string{"\u00e9", "\u4f60\u597d", "\U0001f600", "a"} {
		// Strictly over the limit, or clampText returns the body untouched.
		text := strings.Repeat(filler, messageLimit+64)
		clamped := clampText(text)
		if !utf8.ValidString(clamped) {
			t.Errorf("filler %q produced invalid UTF-8", filler)
		}
		if len(clamped) > messageLimit {
			t.Errorf("filler %q produced %d bytes, over the limit", filler, len(clamped))
		}
		if !strings.HasSuffix(clamped, "[truncated]") {
			t.Errorf("filler %q lost the truncation marker", filler)
		}
	}
}

// TestClampTextPrefersALineBoundary keeps the historical shape: a body with
// newlines is cut at one rather than mid-sentence.
func TestClampTextPrefersALineBoundary(t *testing.T) {
	line := strings.Repeat("x", 100) + "\n"
	clamped := clampText(strings.Repeat(line, 60))
	if !strings.HasSuffix(clamped, "[truncated]") {
		t.Fatalf("the marker is missing")
	}
	body := strings.TrimSuffix(clamped, "\n... [truncated]")
	if !strings.HasSuffix(body, strings.Repeat("x", 100)) {
		t.Errorf("the cut should land on a line boundary, got %q", body[len(body)-20:])
	}
}

func TestBotIDFromToken(t *testing.T) {
	if got := BotIDFromToken("123456:ABC-DEF"); got != "123456" {
		t.Errorf("BotIDFromToken = %q, want 123456", got)
	}
	if got := BotIDFromToken("nodigits"); got != "" {
		t.Errorf("a token without a prefix should yield an empty id, got %q", got)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	if got := RetryAfterSeconds(&APIError{Code: 429, RetryAfter: 12}); got != 12 {
		t.Errorf("RetryAfterSeconds = %d, want 12", got)
	}
	if got := RetryAfterSeconds(nil); got != 0 {
		t.Errorf("nil error should yield 0, got %d", got)
	}
}

func TestHelpTextListsCoreCommands(t *testing.T) {
	help := helpText()
	for _, want := range []string{"/run", "/stop", "/new", "/model", "/status"} {
		if !strings.Contains(help, want) {
			t.Errorf("help text is missing %s", want)
		}
	}
}
