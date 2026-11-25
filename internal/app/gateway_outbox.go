package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/99apps-id/termixgo/internal/secrets"
)

// gatewayOutbox holds admin-only notifications until a transport confirms send.
// A crash after send but before ack can duplicate a notification (at least once).
type gatewayOutbox struct {
	db *sql.DB

	// restartReason is why this process started, set once by beginRun. It is
	// empty on the first run ever and for a store that never called beginRun.
	mu            sync.Mutex
	restartReason string
	announced     bool
}

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
		`CREATE TABLE IF NOT EXISTS gateway_run (id INTEGER PRIMARY KEY CHECK(id=1), version TEXT NOT NULL, boot INTEGER NOT NULL)`,
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
	// A start is not an alert by itself: the alert is sent once the gateway is
	// back, and says why it restarted. A first connection is only a baseline.
	if status == "starting" || ((previous == "" || previous == "first_start") && status == "online") {
		if _, err := tx.Exec(`DELETE FROM gateway_pending_transitions WHERE source=?`, source); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	announce := false
	// The text names the kind of gateway, never the account. An account name
	// such as "raka" is an internal id: it told the reader nothing they could
	// act on and showed one account's name in another's alert.
	label := gatewayLabel(source)
	var body string
	switch status {
	case "offline":
		body = "Gateway alert: " + label + " connection lost and not yet recovered."
	case "online":
		if previous == "starting" {
			// Coming back from a restart: one message per process, with the
			// cause, however many accounts reconnect.
			text, ok := o.restartNotice()
			if !ok {
				if _, err := tx.Exec(`DELETE FROM gateway_pending_transitions WHERE source=?`, source); err != nil {
					return false, err
				}
				return false, tx.Commit()
			}
			body = text
			announce = true
		} else {
			body = "Gateway " + label + " is connected again."
		}
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
	if announce {
		o.mu.Lock()
		o.announced = true
		o.mu.Unlock()
	}
	return true, nil
}

// restartNotice is the one message of a restart, or false when this process
// did not restart (the first run) or has already announced it.
func (o *gatewayOutbox) restartNotice() (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.restartReason == "" || o.announced {
		return "", false
	}
	return "Termixgo restarted " + o.restartReason + "; the gateway is connected.", true
}

// beginRun records this process and works out why it started: a new version
// (an update), a new boot of the machine (the VPS restarted), or neither (the
// service restarted). The first run ever has nothing to compare with and is
// not a restart.
func (o *gatewayOutbox) beginRun(version string, boot int64) error {
	var previousVersion string
	var previousBoot int64
	err := o.db.QueryRow(`SELECT version, boot FROM gateway_run WHERE id=1`).Scan(&previousVersion, &previousBoot)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	reason := ""
	switch {
	case err == sql.ErrNoRows:
	case previousVersion != version:
		reason = fmt.Sprintf("because of an update from version %s to %s", previousVersion, version)
	case boot != 0 && previousBoot != 0 && absInt64(boot-previousBoot) > 120:
		reason = "because the VPS restarted"
	default:
		reason = "because the service restarted"
	}
	if _, err := o.db.Exec(`INSERT INTO gateway_run(id,version,boot) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET version=excluded.version, boot=excluded.boot`, version, boot); err != nil {
		return err
	}
	o.mu.Lock()
	o.restartReason = reason
	o.announced = false
	o.mu.Unlock()
	return nil
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

// gatewayLabel is the reader-facing name of a source ("whatsapp/raka" reads
// "WhatsApp"). The state table still keys on the full source, so two accounts
// of one channel keep separate state.
func gatewayLabel(source string) string {
	kind, _, _ := strings.Cut(source, "/")
	switch strings.ToLower(kind) {
	case "whatsapp":
		return "WhatsApp"
	case "telegram":
		return "Telegram"
	default:
		return kind
	}
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
