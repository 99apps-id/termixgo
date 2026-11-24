package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A tool result that is longer than the model should carry is cut to its head
// and tail. Cutting alone loses the middle for good, so the whole output is
// also written to a file in the workspace and the result names it: the model
// can read the part it needs with read_file (offset and limit) or grep, and the
// context stays small. This is the same idea as keeping a command's full log on
// disk and putting a pointer in the conversation.
const (
	// toolOutputDir is where full outputs go, inside the workspace state
	// directory the rest of Termixgo already uses (snapshots, pastes). The
	// workspace walk skips it.
	toolOutputDir = ".termixgo/tool-output"
	// toolOutputKeep and toolOutputMaxAge bound the folder: the newest files are
	// kept up to the count, and nothing older than the age stays.
	toolOutputKeep   = 60
	toolOutputMaxAge = 7 * 24 * time.Hour
	// toolOutputMaxBytes caps one saved output, so a runaway command cannot fill
	// the disk. What is cut off past it is said in the file's last line.
	toolOutputMaxBytes = 8 << 20
)

// spillToolOutput writes a tool's full output to the workspace and returns its
// workspace-relative path. It returns "" when nothing was written (no
// workspace, an unwritable folder, an empty output): the caller then keeps the
// plain clipped result, which is what it did before.
func spillToolOutput(env *Env, tool, full string) string {
	if env == nil || strings.TrimSpace(env.Workspace) == "" || strings.TrimSpace(full) == "" {
		return ""
	}
	dir := filepath.Join(env.Workspace, filepath.FromSlash(toolOutputDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	name := fmt.Sprintf("%s-%s-%s.txt", time.Now().UTC().Format("20060102-150405"), safeToolName(tool), hex.EncodeToString(suffix))
	path := filepath.Join(dir, name)
	body := full
	note := ""
	if len(body) > toolOutputMaxBytes {
		note = fmt.Sprintf("\n... [stopped at %d bytes; the command printed %d]\n", toolOutputMaxBytes, len(full))
		body = clipBytes(body, toolOutputMaxBytes)
	}
	if err := os.WriteFile(path, []byte(body+note), 0o600); err != nil {
		return ""
	}
	pruneToolOutput(dir)
	return displayPath(env, path)
}

// safeToolName keeps a tool name usable in a file name.
func safeToolName(name string) string {
	var builder strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	if builder.Len() == 0 {
		return "tool"
	}
	return builder.String()
}

// pruneToolOutput removes saved outputs past the age limit and, beyond the
// count limit, the oldest ones. Errors are ignored: a stale file is a nuisance,
// not a failure of the tool that just ran.
func pruneToolOutput(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type saved struct {
		path string
		when time.Time
	}
	var files []saved
	cutoff := time.Now().Add(-toolOutputMaxAge)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
			continue
		}
		files = append(files, saved{path: path, when: info.ModTime()})
	}
	if len(files) <= toolOutputKeep {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].when.After(files[j].when) })
	for _, old := range files[toolOutputKeep:] {
		_ = os.Remove(old.path)
	}
}

// spillNote is the line appended to a clipped result. It names the file and how
// to read a slice of it.
func spillNote(path string, bytes int) string {
	return fmt.Sprintf("\n[The full output (%d bytes) is saved at %s. Read the omitted part with read_file (path, offset, limit) or search it with grep, instead of running the command again.]", bytes, path)
}

// appendSpill saves the full output and adds a pointer to it after the clipped
// text. The caller only asks when the output really was clipped. When the copy
// cannot be saved the clipped text comes back unchanged.
func appendSpill(env *Env, tool, full, clipped string) string {
	path := spillToolOutput(env, tool, full)
	if path == "" {
		return clipped
	}
	return clipped + spillNote(path, len(full))
}
