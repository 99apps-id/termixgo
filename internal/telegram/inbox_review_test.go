package telegram

import (
	"path/filepath"
	"strings"
	"testing"
)

// reviewFixture builds an inbox that holds one update of each state.
func reviewFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inbox.sqlite")
	inbox, err := openDurableInbox(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	message := func(id int64, text string) Update {
		return Update{UpdateID: id, Message: &Message{Chat: Chat{ID: 7, Type: "private"}, From: &User{ID: 9}, Text: text}}
	}
	if err := inbox.accept([]Update{message(1, "uncertain one"), message(2, "queued two"), message(3, "token 123456789:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA leaked")}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{
		`UPDATE updates SET state='uncertain' WHERE id IN (1,3)`,
		`UPDATE updates SET state='running' WHERE id=2`,
	} {
		if _, err := inbox.db.Exec(step); err != nil {
			t.Fatal(err)
		}
	}
	if err := inbox.close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListInboxShowsEveryHeldUpdateWithAnOpenState(t *testing.T) {
	path := reviewFixture(t)
	entries, err := ListInbox(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].State != "uncertain" || entries[0].Kind != "message" || entries[0].ChatID != 7 || entries[0].SenderID != 9 || entries[0].Text != "uncertain one" {
		t.Errorf("first entry = %+v", entries[0])
	}
	if strings.Contains(entries[2].Text, "AAAAAAAA") {
		t.Errorf("a credential reached the preview: %q", entries[2].Text)
	}
}

// Reviewing must not do what starting the bot does: it would turn a live
// handler's running row into an uncertain one.
func TestListInboxLeavesARunningUpdateRunning(t *testing.T) {
	path := reviewFixture(t)
	if _, err := ListInbox(path); err != nil {
		t.Fatal(err)
	}
	entries, _ := ListInbox(path)
	if entries[1].State != "running" {
		t.Fatalf("the running update became %q", entries[1].State)
	}
}

func TestDropInboxRemovesOnlyUncertainUpdates(t *testing.T) {
	path := reviewFixture(t)
	removed, err := DropInbox(path, []int64{1, 2})
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v, want only update 1", removed, err)
	}
	entries, _ := ListInbox(path)
	if len(entries) != 2 || entries[0].ID != 2 || entries[0].State != "running" {
		t.Fatalf("after a targeted drop: %+v", entries)
	}
	removed, err = DropInbox(path, nil)
	if err != nil || removed != 1 {
		t.Fatalf("drop all removed=%d err=%v", removed, err)
	}
	entries, _ = ListInbox(path)
	if len(entries) != 1 || entries[0].State != "running" {
		t.Fatalf("drop all touched a live update: %+v", entries)
	}
}

func TestReviewRefusesAMissingInbox(t *testing.T) {
	if _, err := ListInbox(filepath.Join(t.TempDir(), "none.sqlite")); err == nil {
		t.Fatalf("a missing inbox must be reported, not created")
	}
}

func TestCursorFileName(t *testing.T) {
	cases := map[[2]string]string{
		{"123", "default"}: "telegram-offset-123.txt",
		{"123", ""}:        "telegram-offset-123.txt",
		{"123", "raka"}:    "telegram-offset-123-raka.txt",
	}
	for input, want := range cases {
		if got := CursorFileName(input[0], input[1]); got != want {
			t.Errorf("CursorFileName(%v) = %q, want %q", input, got, want)
		}
	}
}
