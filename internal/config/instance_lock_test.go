package config

import (
	"errors"
	"os"
	"testing"
)

func TestInstanceLockPreventsDuplicate(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())

	lock1, pid1, err := TryAcquireInstanceLock("test-instance.lock")
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if pid1 != os.Getpid() {
		t.Errorf("pid1 = %d, want %d", pid1, os.Getpid())
	}
	defer lock1.Close()

	lock2, pid2, err2 := TryAcquireInstanceLock("test-instance.lock")
	if !errors.Is(err2, ErrAlreadyLocked) {
		t.Fatalf("second acquire should fail with ErrAlreadyLocked, got err=%v", err2)
	}
	if lock2 != nil {
		t.Errorf("lock2 should be nil")
		lock2.Close()
	}
	if pid2 != os.Getpid() {
		t.Errorf("pid2 = %d, want %d", pid2, os.Getpid())
	}

	if err := lock1.Close(); err != nil {
		t.Fatalf("lock1 close: %v", err)
	}

	lock3, pid3, err3 := TryAcquireInstanceLock("test-instance.lock")
	if err3 != nil {
		t.Fatalf("re-acquire after close failed: %v", err3)
	}
	if pid3 != os.Getpid() {
		t.Errorf("pid3 = %d, want %d", pid3, os.Getpid())
	}
	_ = lock3.Close()
}
