//go:build windows

package config

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// openConfigLock opens the sibling lock file the save serialises on. The
// lock lives on a separate file rather than config.json itself because the
// save replaces config.json by rename, which would move the lock with the
// old inode out from under the next waiter.
func openConfigLock(dir string) (*os.File, error) {
	return os.OpenFile(filepath.Join(dir, FileName+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
}

// lockConfigFile takes an exclusive lock over the first byte of the file,
// blocking until it is free. The lock lives on the file's handle, so closing
// it releases the lock even if the process exits without unlocking.
func lockConfigFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped)
}

func unlockConfigFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
