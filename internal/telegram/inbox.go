package telegram

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/99apps-id/termixgo/internal/secrets"
)

// durableInbox records updates before the next poll can acknowledge them. An
// interrupted handler is retained as uncertain rather than automatically run a
// second time, because it may already have performed external side effects.
type durableInbox struct{ db *sql.DB }

func openDurableInbox(path string, initialOffset int64) (*durableInbox, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	// The file holds every update's text. 0600 means nothing on Windows, so the
	// access rules are set the way the secret store sets them.
	// Best effort: a share that refuses the change must not stop the bot.
	_ = secrets.RestrictFile(path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*durableInbox, error) { db.Close(); return nil, err }
	for _, stmt := range []string{
		`PRAGMA journal_mode=DELETE`,
		`PRAGMA synchronous=FULL`,
		`CREATE TABLE IF NOT EXISTS metadata (id INTEGER PRIMARY KEY CHECK(id=1), offset INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS activity (id INTEGER PRIMARY KEY CHECK(id=1), last_update INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS updates (id INTEGER PRIMARY KEY, payload BLOB NOT NULL, state TEXT NOT NULL CHECK(state IN ('queued','running','uncertain')))`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fail(err)
		}
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO metadata(id, offset) VALUES(1, ?)`, initialOffset); err != nil {
		return fail(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO activity(id, last_update) VALUES(1, 0)`); err != nil {
		return fail(err)
	}
	if _, err := db.Exec(`UPDATE updates SET state='uncertain' WHERE state='running'`); err != nil {
		return fail(err)
	}
	return &durableInbox{db: db}, nil
}

func (i *durableInbox) close() error { return i.db.Close() }

func (i *durableInbox) offset() (int64, error) {
	var offset int64
	err := i.db.QueryRow(`SELECT offset FROM metadata WHERE id=1`).Scan(&offset)
	return offset, err
}

// pollOffset omits the offset after prolonged inactivity: Telegram can assign
// a lower random ID after a week without updates. Polling with the old high ID
// would acknowledge those new updates before the inbox ever sees them.
func (i *durableInbox) pollOffset(now time.Time) (int64, error) {
	var offset, last int64
	if err := i.db.QueryRow(`SELECT metadata.offset, activity.last_update FROM metadata JOIN activity ON metadata.id=activity.id WHERE metadata.id=1`).Scan(&offset, &last); err != nil {
		return 0, err
	}
	if last == 0 || now.Sub(time.Unix(last, 0)) >= 6*24*time.Hour {
		return 0, nil
	}
	return offset, nil
}

// accept commits every update and the subsequent polling offset atomically.
func (i *durableInbox) accept(updates []Update) error {
	offset, err := i.offset()
	if err != nil {
		return err
	}
	_, err = i.acceptPolled(updates, offset, time.Now())
	return err
}

// acceptPolled preserves low IDs seen on an offset-less poll as uncertain.
// They can be old redeliveries or new random IDs, so neither automatic replay
// nor dropping them is safe. Reset the polling watermark only after saving.
func (i *durableInbox) acceptPolled(updates []Update, polledOffset int64, now time.Time) (bool, error) {
	tx, err := i.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var offset int64
	if err := tx.QueryRow(`SELECT offset FROM metadata WHERE id=1`).Scan(&offset); err != nil {
		return false, err
	}
	rebase := false
	for _, update := range updates {
		if polledOffset == 0 && update.UpdateID < offset {
			rebase = true
			break
		}
	}
	var newOffset int64
	for _, update := range updates {
		payload := update.raw
		if len(payload) == 0 {
			payload, err = json.Marshal(update)
			if err != nil {
				return false, err
			}
		}
		state := "queued"
		if rebase && update.UpdateID < offset {
			state = "uncertain"
		}
		if rebase {
			var existing []byte
			err := tx.QueryRow(`SELECT payload FROM updates WHERE id=?`, update.UpdateID).Scan(&existing)
			if err != nil && err != sql.ErrNoRows {
				return false, err
			}
			if err == nil && !bytes.Equal(existing, payload) {
				return false, fmt.Errorf("a Telegram update ID collision requires manual review")
			}
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO updates(id,payload,state) SELECT ?,?,? WHERE ? OR ? >= (SELECT offset FROM metadata WHERE id=1)`, update.UpdateID, payload, state, rebase, update.UpdateID); err != nil {
			return false, err
		}
		if update.UpdateID+1 > newOffset {
			newOffset = update.UpdateID + 1
		}
	}
	if len(updates) > 0 {
		if rebase {
			_, err = tx.Exec(`UPDATE metadata SET offset=? WHERE id=1`, newOffset)
		} else {
			_, err = tx.Exec(`UPDATE metadata SET offset=MAX(offset, ?) WHERE id=1`, newOffset)
		}
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(`UPDATE activity SET last_update=? WHERE id=1`, now.Unix()); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return rebase, nil
}

func (i *durableInbox) queued() ([]Update, error) {
	rows, err := i.db.Query(`SELECT payload FROM updates WHERE state='queued' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Update
	for rows.Next() {
		var payload []byte
		var update Update
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &update); err != nil {
			return nil, fmt.Errorf("decode durable Telegram update: %w", err)
		}
		result = append(result, update)
	}
	return result, rows.Err()
}

func (i *durableInbox) claim(id int64) (bool, error) {
	result, err := i.db.Exec(`UPDATE updates SET state='running' WHERE id=? AND state='queued'`, id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (i *durableInbox) finish(id int64, uncertain bool) error {
	if uncertain {
		_, err := i.db.Exec(`UPDATE updates SET state='uncertain' WHERE id=? AND state='running'`, id)
		return err
	}
	_, err := i.db.Exec(`DELETE FROM updates WHERE id=? AND state='running'`, id)
	return err
}

func (i *durableInbox) uncertainCount() (int, error) {
	var count int
	err := i.db.QueryRow(`SELECT count(*) FROM updates WHERE state='uncertain'`).Scan(&count)
	return count, err
}
