// Package whatsapp is the WhatsApp channel: one linked-device account per
// agent, a session store per account, QR or pairing-code login, and the text
// message flow. It talks to WhatsApp through whatsmeow, the same multi-device
// protocol Baileys implements, in-process and pure Go.
package whatsapp

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// SessionPath is where one account's linked-device session lives: inside the
// owning agent's home, so two agents never share a device identity.
func SessionPath(agentHome, account string) string {
	return filepath.Join(agentHome, "channels", "whatsapp", account, "session.db")
}

// FallbackSessionPaths returns potential locations where this WhatsApp account's
// session might have been saved under other agent homes or default paths.
func FallbackSessionPaths(account string) []string {
	cleanAccount := strings.TrimSpace(account)
	if cleanAccount == "" {
		return nil
	}
	var paths []string
	add := func(p string, err error) {
		if err == nil && p != "" {
			paths = append(paths, p)
		}
	}
	add(config.HomePath(filepath.Join("agents", cleanAccount, "channels", "whatsapp", cleanAccount, "session.db")))
	if cleanAccount != config.FinalAgentName {
		add(config.HomePath(filepath.Join("agents", config.FinalAgentName, "channels", "whatsapp", cleanAccount, "session.db")))
	}
	add(config.HomePath(filepath.Join("channels", "whatsapp", cleanAccount, "session.db")))
	return paths
}

// HasLinkedDevice reports whether the SQLite file at path exists and has a registered device with a non-empty JID.
func HasLinkedDevice(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return false
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return false
	}
	defer db.Close()

	var count int
	row := db.QueryRow("SELECT count(*) FROM whatsmeow_device WHERE jid IS NOT NULL AND jid != ''")
	if err := row.Scan(&count); err != nil {
		return false
	}
	return count > 0
}

// CopySessionDB copies a SQLite database file and restricts it.
func CopySessionDB(src, dst string) error {
	if src == dst {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return err
	}
	_ = secrets.RestrictFile(dst)
	return nil
}

// OpenDevice opens the multi-device store of one account, creating the SQLite
// file on first use. The returned closer releases the database.
//
// The session material is as sensitive as a password: the directory is 0700
// and the files are restricted after creation. One file per account is what
// keeps two agents' device sessions apart.
func OpenDevice(ctx context.Context, path string) (*store.Device, func() error, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil, fmt.Errorf("the WhatsApp session path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create the WhatsApp session directory: %w", err)
	}
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open the WhatsApp session store: %w", err)
	}
	container := sqlstore.NewWithDB(db, "sqlite3", waLog.Noop)
	if err := container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("upgrade the WhatsApp session store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("read the WhatsApp device: %w", err)
	}
	if device == nil || device.ID == nil {
		account := filepath.Base(filepath.Dir(path))
		if account != "" && account != "." {
			for _, fallback := range FallbackSessionPaths(account) {
				if filepath.Clean(fallback) != filepath.Clean(path) && HasLinkedDevice(fallback) {
					_ = db.Close()
					if err := CopySessionDB(fallback, path); err == nil {
						return OpenDevice(ctx, path)
					}
					return OpenDevice(ctx, fallback)
				}
			}
		}
		if device == nil {
			device = container.NewDevice()
		}
	}
	// The database files carry the identity keys, so they are restricted the
	// same way the secret store is. Best effort: a read-only share can refuse.
	_ = secrets.RestrictFile(path)
	return device, db.Close, nil
}

// Client builds the whatsmeow client for a device.
func Client(device *store.Device) *whatsmeow.Client {
	cli := whatsmeow.NewClient(device, waLog.Noop)
	cli.SetForceActiveDeliveryReceipts(true)
	cli.AutoTrustIdentity = true
	return cli
}
