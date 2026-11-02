//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"syscall"
)

// openConfigLock opens the sibling lock file the save serialises on. The
// lock lives on a separate file rather than config.json itself because the
// save replaces config.json by rename, which would move the lock with the
// old inode out from under the next waiter.
func openConfigLock(dir string) (*os.File, error) {
	return os.OpenFile(filepath.Join(dir, FileName+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
}

// lockConfigFile takes an exclusive advisory lock, blocking until it is free.
// The kernel releases it when the descriptor closes, so a crashed process
// cannot leave the file locked.
func lockConfigFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

func unlockConfigFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func tryLockInstanceFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockInstanceFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
