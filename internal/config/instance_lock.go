package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrAlreadyLocked reports that another process already holds the lock.
var ErrAlreadyLocked = errors.New("instance is already locked by another process")

// InstanceLock represents an open lock file held exclusively by this process.
type InstanceLock struct {
	file *os.File
	path string
}

// TryAcquireInstanceLock attempts to acquire an exclusive non-blocking lock on a named file in ~/.termixgo.
// If another process holds the lock, it reads the PID from the file and returns nil, existingPID, ErrAlreadyLocked.
// If successful, it writes the current PID into the lock file and returns the InstanceLock.
func TryAcquireInstanceLock(name string) (*InstanceLock, int, error) {
	path, err := HomePath(name)
	if err != nil {
		return nil, 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, 0, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, 0, err
	}
	if err := tryLockInstanceFile(file); err != nil {
		content, _ := io.ReadAll(file)
		_ = file.Close()
		existingPID := 0
		if num, parseErr := strconv.Atoi(strings.TrimSpace(string(content))); parseErr == nil {
			existingPID = num
		}
		return nil, existingPID, ErrAlreadyLocked
	}

	_ = file.Truncate(0)
	_, _ = file.Seek(0, 0)
	_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
	_ = file.Sync()

	return &InstanceLock{file: file, path: path}, os.Getpid(), nil
}

// Close releases the lock and closes the underlying file.
func (l *InstanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = unlockInstanceFile(l.file)
	err := l.file.Close()
	l.file = nil
	_ = os.Remove(l.path)
	return err
}

// Release is an alias for Close.
func (l *InstanceLock) Release() error {
	return l.Close()
}
