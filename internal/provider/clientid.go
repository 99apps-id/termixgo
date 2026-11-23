package provider

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

// uuidV7 returns a time-ordered UUID, the id shape the Codex client puts on
// its session, thread and turn headers.
func uuidV7() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		now := uint64(time.Now().UnixNano())
		for i := range raw {
			raw[i] = byte(now >> (uint(i%8) * 8))
		}
	}
	ms := uint64(time.Now().UnixMilli())
	raw[0], raw[1], raw[2] = byte(ms>>40), byte(ms>>32), byte(ms>>24)
	raw[3], raw[4], raw[5] = byte(ms>>16), byte(ms>>8), byte(ms)
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

var (
	installationOnce sync.Once
	installationID   string
)

// installationUUID is the stable per-install id the official clients send
// beside the session. It is generated once, kept in the state directory with
// private permissions, and never taken from another tool's installation, so
// Termixgo is one consistent install rather than a copy of someone else's.
func installationUUID() string {
	installationOnce.Do(func() {
		path, err := config.HomePath("installation_id")
		if err == nil {
			if data, readErr := os.ReadFile(path); readErr == nil {
				if id := strings.TrimSpace(string(data)); len(id) == 36 {
					installationID = id
					return
				}
			}
		}
		installationID = uuidV7()
		if err == nil {
			if _, dirErr := config.EnsureHome(); dirErr == nil {
				_ = os.WriteFile(path, []byte(installationID+"\n"), 0o600)
			}
		}
	})
	return installationID
}
