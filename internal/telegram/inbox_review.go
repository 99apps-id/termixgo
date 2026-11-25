package telegram

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/99apps-id/termixgo/internal/channel"
)

// CursorFileName is the offset file of one bot. The durable inbox sits beside
// it, so the app that starts the bot and the command that reviews the inbox
// derive the path from one function and cannot drift apart.
func CursorFileName(botID, account string) string {
	file := "telegram-offset-" + botID
	if account != "" && account != "default" {
		file += "-" + account
	}
	return file + ".txt"
}

// InboxPath is where the durable inbox of a cursor file lives.
func InboxPath(cursorPath string) string { return cursorPath + ".inbox.sqlite" }

// InboxEntry is one update still held by the durable inbox.
type InboxEntry struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
	// Kind is "message" or "callback".
	Kind     string `json:"kind"`
	ChatID   int64  `json:"chatId,omitempty"`
	SenderID int64  `json:"senderId,omitempty"`
	// Text is a short, redacted preview of what the sender wrote.
	Text string `json:"text,omitempty"`
}

// openInboxForReview opens an existing inbox without the recovery steps the
// bot runs on start. Opening it the way the bot does would turn the rows a
// live service is handling right now into uncertain ones.
func openInboxForReview(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("there is no durable inbox at %s; the bot has not run with it yet", path)
		}
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// ListInbox returns the updates the inbox holds that are not finished: those
// waiting to run, the one running, and the interrupted ones that need review.
func ListInbox(path string) ([]InboxEntry, error) {
	db, err := openInboxForReview(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, state, payload FROM updates ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []InboxEntry
	for rows.Next() {
		var entry InboxEntry
		var payload []byte
		if err := rows.Scan(&entry.ID, &entry.State, &payload); err != nil {
			return nil, err
		}
		var update Update
		if json.Unmarshal(payload, &update) == nil {
			switch {
			case update.Message != nil:
				entry.Kind = "message"
				entry.ChatID = update.Message.Chat.ID
				if update.Message.From != nil {
					entry.SenderID = update.Message.From.ID
				}
				entry.Text = inboxPreview(update.Message.Text, update.Message.Caption)
			case update.CallbackQuery != nil:
				entry.Kind = "callback"
				if update.CallbackQuery.From != nil {
					entry.SenderID = update.CallbackQuery.From.ID
				}
				if update.CallbackQuery.Message != nil {
					entry.ChatID = update.CallbackQuery.Message.Chat.ID
				}
			}
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// DropInbox deletes interrupted updates the operator has reviewed. Only the
// uncertain ones can be dropped: a queued update is still going to run and a
// running one belongs to a live handler. An empty ids list drops every
// uncertain update. It returns how many were removed.
func DropInbox(path string, ids []int64) (int, error) {
	db, err := openInboxForReview(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if len(ids) == 0 {
		result, err := db.Exec(`DELETE FROM updates WHERE state='uncertain'`)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		return int(n), err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	removed := 0
	for _, id := range ids {
		result, err := tx.Exec(`DELETE FROM updates WHERE id=? AND state='uncertain'`, id)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		removed += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}

// inboxPreview is the first words of an update, with anything that looks like
// a credential removed: the review output goes to a terminal and to logs.
func inboxPreview(parts ...string) string {
	text := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	text = channel.Redact(text, nil)
	if utf8.RuneCountInString(text) > 80 {
		text = string([]rune(text)[:79]) + "..."
	}
	return text
}
