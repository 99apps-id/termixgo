package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/99apps-id/termixgo/internal/secrets"
)

// gatewayOutbox holds admin-only notifications until a transport confirms send.
// A crash after send but before ack can duplicate a notification (at least once).
type gatewayOutbox struct{ db *sql.DB }

type gatewayDelivery struct {
	id     int64
	target gatewayAdminTarget
	text   string
}

func openGatewayOutbox(path string) (*gatewayOutbox, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	// Best effort: a share that refuses the change must not stop the bot.
	_ = secrets.RestrictFile(path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`PRAGMA busy_timeout=5000`,
		`PRAGMA journal_mode=DELETE`,
		`PRAGMA synchronous=FULL`,
		`CREATE TABLE IF NOT EXISTS gateway_state (source TEXT PRIMARY KEY, status TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS gateway_outbox (id INTEGER PRIMARY KEY, channel TEXT NOT NULL, account TEXT NOT NULL, recipient TEXT NOT NULL, body TEXT NOT NULL, created INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS gateway_pending_transitions (source TEXT PRIMARY KEY, status TEXT NOT NULL, updated INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &gatewayOutbox{db: db}, nil
}

// transition atomically records state and enqueues per-admin notifications.
// Initial online is a baseline, while a return from offline/starting is an alert.
func (o *gatewayOutbox) transition(source, status string, targets []gatewayAdminTarget) (bool, error) {
	if status != "online" && status != "offline" && status != "starting" {
		return false, fmt.Errorf("invalid gateway status")
	}
	tx, err := o.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRow(`SELECT status FROM gateway_state WHERE source=?`, source).Scan(&previous)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if previous == status {
		if _, err := tx.Exec(`DELETE FROM gateway_pending_transitions WHERE source=?`, source); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	if previous == "" && status == "starting" {
		if _, err := tx.Exec(`INSERT INTO gateway_state(source,status) VALUES(?, 'first_start')`, source); err != nil {
			return false, err
		}
		if _, err := tx.Exec(`DELETE FROM gateway_pending_transitions WHERE source=?`, source); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	if _, err := tx.Exec(`INSERT INTO gateway_state(source,status) VALUES(?,?) ON CONFLICT(source) DO UPDATE SET status=excluded.status`, source, status); err != nil {
		return false, err
	}
	if (previous == "" || previous == "first_start") && status == "online" {
		if _, err := tx.Exec(`DELETE FROM gateway_pending_transitions WHERE source=?`, source); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	var body string
	switch status {
	case "offline":
		body = "Peringatan gateway " + source + ": koneksi terputus."
	case "online":
		body = "Gateway " + source + " tersambung kembali."
	case "starting":
		body = "Gateway " + source + " dimulai ulang; menunggu koneksi."
	}
	if len(targets) == 0 {
		return false, fmt.Errorf("gateway notification has no configured private admin target")
	}
	var pending int
	if err := tx.QueryRow(`SELECT count(*) FROM gateway_outbox`).Scan(&pending); err != nil {
		return false, err
	}
	if pending+len(targets) > 5000 {
		return false, fmt.Errorf("gateway notification outbox full")
	}
	for _, target := range targets {
		if _, err := tx.Exec(`INSERT INTO gateway_outbox(channel,account,recipient,body,created) VALUES(?,?,?,?,?)`, target.channel, target.account, target.user, body, time.Now().Unix()); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM gateway_pending_transitions WHERE source=?`, source); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

type gatewayPendingTransition struct {
	source string
	status string
}

// deferTransition persists a failed transition so it can be retried once admin
// targets are configured or outbox capacity frees up.
func (o *gatewayOutbox) deferTransition(source, status string) error {
	if status != "online" && status != "offline" && status != "starting" {
		return fmt.Errorf("invalid gateway status")
	}
	_, err := o.db.Exec(
		`INSERT INTO gateway_pending_transitions(source,status,updated) VALUES(?,?,?) ON CONFLICT(source) DO UPDATE SET status=excluded.status, updated=excluded.updated`,
		source, status, time.Now().Unix(),
	)
	return err
}

func (o *gatewayOutbox) pendingTransitions() ([]gatewayPendingTransition, error) {
	rows, err := o.db.Query(`SELECT source,status FROM gateway_pending_transitions ORDER BY updated, source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var transitions []gatewayPendingTransition
	for rows.Next() {
		var t gatewayPendingTransition
		if err := rows.Scan(&t.source, &t.status); err != nil {
			return nil, err
		}
		transitions = append(transitions, t)
	}
	return transitions, rows.Err()
}

func (o *gatewayOutbox) pending() ([]gatewayDelivery, error) {
	rows, err := o.db.Query(`SELECT id,channel,account,recipient,body FROM gateway_outbox ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deliveries []gatewayDelivery
	for rows.Next() {
		var d gatewayDelivery
		if err := rows.Scan(&d.id, &d.target.channel, &d.target.account, &d.target.user, &d.text); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

func (o *gatewayOutbox) ack(id int64) error {
	_, err := o.db.Exec(`DELETE FROM gateway_outbox WHERE id=?`, id)
	return err
}
