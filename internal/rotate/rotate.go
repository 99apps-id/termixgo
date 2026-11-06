// Package rotate appends JSONL records to a file with a size cap. When a file
// passes the cap it is renamed aside and a new one starts, keeping a fixed
// number of archives behind it.
//
// A daemon that never restarts writes these ledgers for months: the audit log,
// the usage ledger and the cron history. Without a cap they are a disk
// exhaustion bug waiting for a small VPS, and a full disk takes the whole
// assistant down rather than only the log.
package rotate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// DefaultMaxBytes is the size one ledger may reach before it is rotated.
	DefaultMaxBytes int64 = 8 << 20
	// DefaultKeep is how many rotated files are kept beside the live one.
	DefaultKeep = 3
)

// Options overrides the defaults.
type Options struct {
	MaxBytes int64
	Keep     int
}

// mu serialises rotation across every ledger in the process. Two writers of the
// same file would otherwise race between the stat and the rename and lose a
// record.
//
// A second process (a CLI command beside `serve`) can still rotate the same
// file at the same moment. That is accepted: the append is atomic and the worst
// case is an archive that starts a moment early, never a lost live file.
var mu sync.Mutex

// Append writes one record and rotates first when the file would pass its cap.
func Append(path string, line []byte, opts Options) error {
	if path == "" {
		return errors.New("the log path is empty")
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	keep := opts.Keep
	if keep <= 0 {
		keep = DefaultKeep
	}
	record := line
	if len(record) == 0 || record[len(record)-1] != '\n' {
		record = append(append([]byte(nil), record...), '\n')
	}

	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	if info, err := os.Stat(path); err == nil {
		if info.Size() > 0 && info.Size()+int64(len(record)) > maxBytes {
			if err := rotateLocked(path, keep); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", filepath.Base(path), err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	if _, err := file.Write(record); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return file.Close()
}

// rotateLocked shifts the archives up one slot and starts a fresh live file.
// Called with mu held.
func rotateLocked(path string, keep int) error {
	if keep < 1 {
		keep = 1
	}
	// The oldest archive falls off first, then each one moves up a slot.
	if err := os.Remove(archivePath(path, keep)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("drop the oldest archive: %w", err)
	}
	for index := keep - 1; index >= 1; index-- {
		from := archivePath(path, index)
		if _, err := os.Stat(from); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("inspect archive: %w", err)
		}
		if err := os.Rename(from, archivePath(path, index+1)); err != nil {
			return fmt.Errorf("rotate archive: %w", err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect live log: %w", err)
	}
	if err := os.Rename(path, archivePath(path, 1)); err != nil {
		return fmt.Errorf("rotate live log: %w", err)
	}
	return nil
}

func archivePath(path string, index int) string {
	return fmt.Sprintf("%s.%d", path, index)
}

// Reader streams the archives oldest first and then the live file, so a caller
// that aggregates history (usage, cron, audit) sees the whole stream without
// loading it into memory. A missing file is an empty reader, not an error.
func Reader(path string) (io.ReadCloser, error) {
	mu.Lock()
	defer mu.Unlock()

	var readers []io.Reader
	var closers []io.Closer
	cleanup := func() {
		for _, closer := range closers {
			_ = closer.Close()
		}
	}
	for index := DefaultKeep; index >= 1; index-- {
		file, err := os.Open(archivePath(path, index))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			cleanup()
			return nil, fmt.Errorf("open archive: %w", err)
		}
		readers = append(readers, file)
		closers = append(closers, file)
	}
	live, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			cleanup()
			return nil, fmt.Errorf("open log: %w", err)
		}
	} else {
		readers = append(readers, live)
		closers = append(closers, live)
	}
	if len(readers) == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return &multiReadCloser{reader: io.MultiReader(readers...), closers: closers}, nil
}

type multiReadCloser struct {
	reader  io.Reader
	closers []io.Closer
}

func (m *multiReadCloser) Read(buffer []byte) (int, error) { return m.reader.Read(buffer) }

func (m *multiReadCloser) Close() error {
	var firstErr error
	for _, closer := range m.closers {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
