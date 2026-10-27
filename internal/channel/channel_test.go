package channel

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestDecideAppliesAccountPolicy(t *testing.T) {
	enabled := config.ChannelAccount{
		Enabled:     true,
		Admins:      []string{"999"},
		DMPolicy:    config.DMPolicyAllowlist,
		AllowFrom:   []string{"6281234"},
		GroupPolicy: config.GroupPolicyMention,
	}
	disabled := enabled
	disabled.Enabled = false

	cases := []struct {
		name   string
		policy config.ChannelAccount
		in     Request
		allow  bool
		admin  bool
		reason string
	}{
		{"admin always speaks", enabled, Request{SenderID: "999"}, true, true, "admin"},
		{"allowlisted dm", enabled, Request{SenderID: "+62 812-34"}, true, false, "allowlisted"},
		{"stranger dm", enabled, Request{SenderID: "555"}, false, false, "sender is not on the allowlist"},
		{"group mention", enabled, Request{IsGroup: true, ChatID: "-100", SenderID: "555", MentionsBot: true}, true, false, "group"},
		{"group without mention", enabled, Request{IsGroup: true, ChatID: "-100", SenderID: "555"}, false, false, "group message without a mention"},
		{"open dm", config.ChannelAccount{Enabled: true, DMPolicy: config.DMPolicyOpen}, Request{SenderID: "555"}, true, false, "open DMs"},
		{"denied dm", config.ChannelAccount{Enabled: true, DMPolicy: config.DMPolicyDeny}, Request{SenderID: "555"}, false, false, "DMs are denied"},
		{"pairing waits", config.ChannelAccount{Enabled: true, DMPolicy: config.DMPolicyPairing}, Request{SenderID: "555"}, false, false, "waiting for pairing approval"},
		{"group replies off", config.ChannelAccount{Enabled: true, GroupPolicy: config.GroupPolicyDisabled}, Request{IsGroup: true, MentionsBot: true}, false, false, "group replies are disabled"},
		{"group all without mention", config.ChannelAccount{Enabled: true, GroupPolicy: config.GroupPolicyAll}, Request{IsGroup: true, ChatID: "-100"}, true, false, "group"},
		{"group allowlist", config.ChannelAccount{Enabled: true, GroupPolicy: config.GroupPolicyAll, GroupAllowFrom: []string{"-200"}}, Request{IsGroup: true, ChatID: "-100"}, false, false, "group is not on the allowlist"},
		{"disabled account", disabled, Request{SenderID: "999"}, false, false, "account disabled"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			decision := Decide(testCase.policy, testCase.in)
			if decision.Allow != testCase.allow || decision.Admin != testCase.admin || decision.Reason != testCase.reason {
				t.Errorf("Decide = %+v, want allow=%v admin=%v reason=%q", decision, testCase.allow, testCase.admin, testCase.reason)
			}
		})
	}
}

func TestCanonicalIDAndLists(t *testing.T) {
	if CanonicalID("+62 812-345") != CanonicalID("62812345") {
		t.Errorf("canonical forms must fold spaces, dashes and the leading plus")
	}
	list := AddID([]string{"6281"}, "+6 281")
	if len(list) != 1 {
		t.Errorf("AddID must not duplicate an existing identity: %v", list)
	}
	list = AddID(list, "6282")
	if len(list) != 2 {
		t.Errorf("AddID lost the new identity: %v", list)
	}
	if got := RemoveID(list, "62-82"); len(got) != 1 || got[0] != "6281" {
		t.Errorf("RemoveID = %v", got)
	}
}

func TestSilent(t *testing.T) {
	silent := []string{"", "   ", "[SILENT]", "(silent)", "*(silent)*", "HEARTBEAT_OK", "no reply."}
	for _, text := range silent {
		if !Silent(text) {
			t.Errorf("Silent(%q) = false, want true", text)
		}
	}
	loud := []string{"silent mode is off", "the answer is 42", strings.Repeat("x", 100)}
	for _, text := range loud {
		if Silent(text) {
			t.Errorf("Silent(%q) = true, want false", text)
		}
	}
}

func TestChunkSplitsOnBoundariesAndRunes(t *testing.T) {
	if chunks := Chunk("   ", 100); chunks != nil {
		t.Errorf("an empty answer must produce no chunks: %v", chunks)
	}
	paragraph := strings.Repeat("kata ", 30) // ~150 bytes
	chunks := Chunk(paragraph+"\n\n"+paragraph, 100)
	if len(chunks) < 2 {
		t.Fatalf("a long answer must be split: %v", chunks)
	}
	for _, chunk := range chunks {
		if len(chunk) > 100 {
			t.Errorf("chunk exceeds the limit (%d bytes): %q", len(chunk), chunk)
		}
	}

	// A cut must never land inside a multi-byte rune: the transport rejects an
	// invalid UTF-8 body and the whole chunk would be lost.
	wide := strings.Repeat("é", 60) // 120 bytes
	for _, chunk := range Chunk(wide, 7) {
		if !utf8.ValidString(chunk) {
			t.Errorf("chunk is not valid UTF-8: %q", chunk)
		}
	}
}

func TestPairingStoreLifecycle(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	path, err := DefaultPairingPath()
	if err != nil {
		t.Fatalf("DefaultPairingPath: %v", err)
	}
	store, err := OpenPairingStore(path)
	if err != nil {
		t.Fatalf("OpenPairingStore: %v", err)
	}
	first, created := store.Request(config.ChannelWhatsApp, "sales", "628123", "Budi")
	if !created {
		t.Fatalf("the first request must be new")
	}
	if first.Code == "" {
		t.Fatalf("a request must carry a code")
	}
	again, createdAgain := store.Request(config.ChannelWhatsApp, "sales", "+62 8123", "Budi")
	if createdAgain {
		t.Errorf("a repeated request must not create a new entry")
	}
	if again.Code != first.Code {
		t.Errorf("asking twice must return the same code: %q vs %q", again.Code, first.Code)
	}
	if pending := store.Pending(); len(pending) != 1 {
		t.Fatalf("Pending = %+v", pending)
	}

	// A reopened store sees the request, which is what lets the CLI approve a
	// pairing that a running serve process created.
	reopened, err := OpenPairingStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if pending := reopened.Pending(); len(pending) != 1 {
		t.Fatalf("the request did not survive a reopen: %+v", pending)
	}
	approved, ok := reopened.Approve(first.Code)
	if !ok || approved.SenderID != "628123" {
		t.Fatalf("Approve = %+v ok=%v", approved, ok)
	}
	if pending := reopened.Pending(); len(pending) != 0 {
		t.Errorf("an approved request must be removed: %+v", pending)
	}
	if _, ok := reopened.Deny("000000"); ok {
		t.Errorf("denying an unknown code must report nothing")
	}
}
