package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestInboxCommandNeedsAnActionAndRefusesUnknownOnes(t *testing.T) {
	withState(t)
	var out bytes.Buffer
	if err := inboxCommand(nil, &out); err == nil {
		t.Errorf("no action must print the usage")
	}
	if err := inboxCommand([]string{"restart"}, &out); err == nil || !strings.Contains(err.Error(), "list or drop") {
		t.Errorf("unknown action: %v", err)
	}
	if err := inboxCommand([]string{"list", "--bogus"}, &out); err == nil {
		t.Errorf("an unknown option must be refused")
	}
	// No token is stored in a fresh state, so the account cannot be found.
	if err := inboxCommand([]string{"list"}, &out); err == nil || !strings.Contains(err.Error(), "no Telegram bot token") {
		t.Errorf("a missing token must say so: %v", err)
	}
}

func TestDropInboxValidatesTheIds(t *testing.T) {
	var out bytes.Buffer
	if err := dropInbox("unused", "default", nil, &out); err == nil {
		t.Errorf("drop with nothing named must be refused")
	}
	if err := dropInbox("unused", "default", []string{"abc"}, &out); err == nil || !strings.Contains(err.Error(), "not an update id") {
		t.Errorf("a non-numeric id: %v", err)
	}
}
